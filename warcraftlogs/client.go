package warcraftlogs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

const (
	tokenURL   = "https://www.warcraftlogs.com/oauth/token"
	graphQLURL = "https://www.warcraftlogs.com/api/v2/client"
)

type tokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type gqlReq struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type ReportsData struct {
	ReportData ReportDataContainer `json:"reportData"`
}

type ReportDataContainer struct {
	Reports ReportsList `json:"reports"`
}

type ReportsList struct {
	Data []Report `json:"data"`
}

type Report struct {
	Code      string `json:"code"`
	Title     string `json:"title"`
	StartTime int64  `json:"startTime"`
	EndTime   int64  `json:"endTime"`
	Owner     Owner  `json:"owner"`
	Zone      Zone   `json:"zone"`
}

type Owner struct {
	Name string `json:"name"`
}

type Zone struct {
	Name         string       `json:"name"`
	Difficulties []Difficulty `json:"difficulties"`
}

type Difficulty struct {
	Name  string `json:"name"`
	Sizes []int  `json:"sizes"`
}

type Client struct {
	clientID     string
	clientSecret string

	resty *resty.Client

	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

func NewClient(wlClientId, wlClientSecret string) (*Client, error) {
	r := resty.New()

	c := &Client{
		clientID:     wlClientId,
		clientSecret: wlClientSecret,
		resty:        r,
	}
	if err := c.refreshToken(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

const tokenSkew = 60 * time.Second

func (c *Client) ensureToken(ctx context.Context) error {
	c.mu.RLock()
	tok := c.token
	exp := c.expiresAt
	c.mu.RUnlock()

	if tok != "" && time.Now().Add(tokenSkew).Before(exp) {
		return nil
	}
	return c.refreshToken(ctx)
}

func (c *Client) refreshToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Add(tokenSkew).Before(c.expiresAt) {
		return nil
	}

	tok, exp, err := getToken(ctx, c.resty, c.clientID, c.clientSecret)
	if err != nil {
		return err
	}
	c.token = tok
	c.expiresAt = exp
	return nil
}

func (c *Client) FindReports(ctx context.Context, guildId int64, startTime time.Time) ([]Report, error) {
	query := `
query($guildID: Int!, $limit:Int!, $startTime: Float!){
  reportData {
    reports(guildID: $guildID, limit: $limit, startTime: $startTime) {
      data {
        code
        title
        startTime
        endTime
        owner {
          name
        }
        zone {
          name
          difficulties {
            name
            sizes
          }
        }
      }
    }
  }
}`
	vars := map[string]interface{}{
		"guildID":   guildId,
		"startTime": float64(startTime.UnixMilli()),
		"limit":     10,
	}
	var out ReportsData
	if err := c.gql(ctx, query, vars, &out); err != nil {
		return nil, err
	}
	return out.ReportData.Reports.Data, nil
}

func getToken(ctx context.Context, r *resty.Client, clientID, clientSecret string) (string, time.Time, error) {
	var tr tokenResp
	resp, err := r.R().
		SetContext(ctx).
		SetBasicAuth(clientID, clientSecret).
		SetHeader("Content-Type", "application/x-www-form-urlencoded").
		SetFormData(map[string]string{"grant_type": "client_credentials"}).
		SetResult(&tr).
		Post(tokenURL)
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.IsError() {
		return "", time.Time{}, fmt.Errorf("oauth token failed: %s: %s", resp.Status(), string(resp.Body()))
	}
	exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return tr.AccessToken, exp, nil
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

func (c *Client) gql(ctx context.Context, query string, vars map[string]interface{}, out any) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}

	reqBody := gqlReq{Query: query, Variables: vars}

	doOnce := func() (*resty.Response, error) {
		c.mu.RLock()
		tok := c.token
		c.mu.RUnlock()

		var env gqlEnvelope
		resp, err := c.resty.R().
			SetContext(ctx).
			SetAuthToken(tok).
			SetHeader("Content-Type", "application/json").
			SetBody(reqBody).
			SetResult(&env).
			Post(graphQLURL)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}

	resp, err := doOnce()
	if err != nil {
		return err
	}

	// Retry once on 401
	if resp.StatusCode() == 401 {
		if err := c.refreshToken(ctx); err != nil {
			return fmt.Errorf("token refresh after 401 failed: %w", err)
		}
		resp, err = doOnce()
		if err != nil {
			return err
		}
	}

	if resp.IsError() {
		return fmt.Errorf("graphql %s: %s", resp.Status(), string(resp.Body()))
	}

	env := resp.Result().(*gqlEnvelope)
	if len(env.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", env.Errors[0].Message)
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("graphql: empty data")
	}
	return json.Unmarshal(env.Data, out)
}

type fightsResp struct {
	ReportData struct {
		Report struct {
			Fights []Fight `json:"fights"`
		} `json:"report"`
	} `json:"reportData"`
}

type Fight struct {
	ID          int    `json:"id"`
	EncounterID int    `json:"encounterID"`
	Name        string `json:"name"`
	StartTime   int64  `json:"startTime"`
	EndTime     int64  `json:"endTime"`
	Difficulty  int    `json:"difficulty"`
	Kill        bool   `json:"kill"`
}

type eventsPage struct {
	ReportData struct {
		Report struct {
			Events struct {
				Data              []json.RawMessage `json:"data"`
				NextPageTimestamp *float64          `json:"nextPageTimestamp"`
			} `json:"events"`
		} `json:"report"`
	} `json:"reportData"`
}

type DeathEvent struct {
	Timestamp int64  `json:"timestamp"`
	Type      string `json:"type"`
	Target    struct {
		Name   string `json:"name"`
		Server string `json:"server"`
	} `json:"target,omitempty"`
}

func (c *Client) GetBossFights(ctx context.Context, reportCode string) ([]Fight, error) {
	const q = `
query($code: String!) {
  reportData {
    report(code: $code) {
      fights(killType: Encounters) {
        id
        encounterID
        name
        startTime
        endTime
        difficulty
        kill
      }
    }
  }
}`
	var out fightsResp
	if err := c.gql(ctx, q, map[string]interface{}{"code": reportCode}, &out); err != nil {
		return nil, err
	}
	return out.ReportData.Report.Fights, nil
}

type PlayerTop struct {
	Name  string
	Value int
}

type ReportDetails struct {
	TopDeaths      []PlayerTop
	TopFirstDeaths []PlayerTop
}

func (c *Client) TopDeathsForReport(ctx context.Context, reportCode string, wipeCutoff int64) (ReportDetails, error) {
	fights, err := c.GetBossFights(ctx, reportCode)
	if err != nil {
		return ReportDetails{}, err
	}
	if len(fights) == 0 {
		return ReportDetails{}, nil
	}

	var (
		totalDeaths []PlayerTop
		firstDeaths []PlayerTop

		totalIdx = make(map[string]int) // name -> index in totalDeaths
		firstIdx = make(map[string]int) // name -> index in firstDeaths
	)

	inc := func(list *[]PlayerTop, idx map[string]int, name string) {
		if name == "" {
			return
		}
		if i, ok := idx[name]; ok {
			(*list)[i].Value++
			return
		}
		idx[name] = len(*list)
		*list = append(*list, PlayerTop{Name: name, Value: 1})
	}

	for _, f := range fights {
		events, err := c.getDeathEvents(ctx, reportCode, f.ID, wipeCutoff)
		if err != nil {
			return ReportDetails{}, fmt.Errorf("events for fight %d: %w", f.ID, err)
		}

		firstTaken := false
		for _, ev := range events {
			name := ev.Target.Name
			if name == "" {
				continue
			}
			inc(&totalDeaths, totalIdx, name)
			if !firstTaken {
				inc(&firstDeaths, firstIdx, name)
				firstTaken = true
			}
		}
	}

	sort.SliceStable(totalDeaths, func(i, j int) bool { return totalDeaths[i].Value > totalDeaths[j].Value })
	sort.SliceStable(firstDeaths, func(i, j int) bool { return firstDeaths[i].Value > firstDeaths[j].Value })

	const N = 5
	if len(totalDeaths) > N {
		totalDeaths = totalDeaths[:N]
	}
	if len(firstDeaths) > N {
		firstDeaths = firstDeaths[:N]
	}

	return ReportDetails{
		TopDeaths:      totalDeaths,
		TopFirstDeaths: firstDeaths,
	}, nil
}

func (c *Client) getDeathEvents(ctx context.Context, reportCode string, fightId int, wipeCutoff int64) ([]DeathEvent, error) {
	q := `
query($code: String!, $fightId: Int!, $wipeCutoff: Int!, $startTime: Float) {
  reportData {
    report(code: $code) {
      events(
        dataType: Deaths
        hostilityType: Friendlies
        killType: Encounters
        fightIDs: [$fightId]
        limit: 1000
        useAbilityIDs: true
        useActorIDs: false
        wipeCutoff: $wipeCutoff
        startTime: $startTime
      ) {
        data
        nextPageTimestamp
      }
    }
  }
}`

	var (
		deaths        []DeathEvent
		pageTimestamp *float64
		pageCount     int
		maxPages      = 10
	)

	for {
		vars := map[string]interface{}{
			"code":       reportCode,
			"fightId":    fightId,
			"wipeCutoff": wipeCutoff,
		}
		if pageTimestamp != nil {
			vars["startTime"] = *pageTimestamp
		}

		var out eventsPage
		if err := c.gql(ctx, q, vars, &out); err != nil {
			return nil, err
		}

		evs := out.ReportData.Report.Events
		for _, raw := range evs.Data {
			var ev DeathEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				slog.Warn("failed to unmarshal DeathEvent", "error", err)
				continue
			}
			deaths = append(deaths, ev)
		}

		if evs.NextPageTimestamp == nil {
			break
		}
		ts := *evs.NextPageTimestamp
		pageTimestamp = &ts

		pageCount++
		if pageCount >= maxPages {
			slog.Warn("pagination aborted: exceeded max pages", "maxPages", maxPages)
			break
		}
	}

	sort.Slice(deaths, func(i, j int) bool {
		return deaths[i].Timestamp < deaths[j].Timestamp
	})

	return deaths, nil
}
