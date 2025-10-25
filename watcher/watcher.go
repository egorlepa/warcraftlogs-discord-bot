package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"bot/storage"
	"bot/warcraftlogs"

	"github.com/jellydator/ttlcache/v3"
)

type LogStartEvent struct {
	Server storage.Server
	Id     string
	Url    string
}

type LogEndEvent struct {
	Server storage.Server
	Id     string
}

type TopDude struct {
	Name  string
	Value string
}

type StatsEvent struct {
	Server        storage.Server
	ReportId      string
	Title         string
	Zone          string
	URL           string
	Live          bool
	TopDPS        []warcraftlogs.PlayerTop
	TopHPS        []warcraftlogs.PlayerTop
	TopDeath      []warcraftlogs.PlayerTop
	TopFirstDeath []warcraftlogs.PlayerTop
	StartedBy     string
	StartedAt     time.Time
	LastUpload    time.Time
}

type Watcher struct {
	wlClient *warcraftlogs.Client
	handler  func(se StatsEvent)
	watched  sync.Map
}

func New(wlClient *warcraftlogs.Client) *Watcher {
	return &Watcher{wlClient: wlClient}
}

func (w *Watcher) Watch(server storage.Server) {
	ctx, cancel := context.WithCancel(context.Background())
	_, isLoaded := w.watched.LoadOrStore(server.ServerId, cancel)
	if !isLoaded {
		go w.watchLoop(ctx, server)
	}
}

type CachedReport struct {
	code    string
	endTime int64
	isLive  bool
}

func (w *Watcher) watchLoop(ctx context.Context, server storage.Server) {
	logger := slog.With("server", server.ServerId)

	reportsCache := ttlcache.New[string, CachedReport](
		ttlcache.WithTTL[string, CachedReport](1 * time.Hour),
	)
	go reportsCache.Start()

	jitter := rand.IntN(10000)
	after := time.After(time.Duration(jitter) * time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			logger.Info("watch loop is stopped")
			return
		case <-after:
			w.checkChanges(ctx, logger, server, reportsCache)
			after = time.After(1 * time.Minute)
		}
	}
}

func (w *Watcher) checkChanges(ctx context.Context, logger *slog.Logger, server storage.Server, reportsCache *ttlcache.Cache[string, CachedReport]) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	start := time.Now()
	reports, err := w.wlClient.FindReports(ctx, server.WlGuildId, time.Now().Add(-12*time.Hour))
	if err != nil {
		logger.Error("error loading guild reports", slog.Int64("guild", server.WlGuildId), "error", err)
		return
	}

	reports = deleteNonRaid(reports)
	logger.Info("loaded reports", "len", len(reports), "duration", time.Since(start).Truncate(time.Millisecond))

	for _, report := range reports {
		isOutdated := time.Since(time.UnixMilli(report.EndTime)) > 15*time.Minute

		isInCache := false
		cachedReport := CachedReport{}
		cacheItem := reportsCache.Get(report.Code)
		if cacheItem != nil {
			isInCache = true
			cachedReport = cacheItem.Value()
		}

		switch {
		case !isInCache:
			switch {
			case isOutdated:
				logger.Info("old report, skipping", "report", report.Code)
			default:
				start := time.Now()
				details, err := w.wlClient.TopDeathsForReport(ctx, report.Code, server.WipeCutoff)
				if err != nil {
					logger.Error("error fetching report details", "report", report.Code)
					continue
				}
				logger.Info("loaded report details", "report", report.Code, "duration", time.Since(start).Truncate(time.Millisecond))
				logger.Info("new live report, sending updates", "report", report.Code)
				w.sendUpdate(ctx, server, true, report, details)
				lr := CachedReport{code: report.Code, endTime: report.EndTime, isLive: true}
				reportsCache.Set(report.Code, lr, ttlcache.DefaultTTL)
			}
		case isInCache:
			switch {
			case cachedReport.endTime != report.EndTime:
				start := time.Now()
				details, err := w.wlClient.TopDeathsForReport(ctx, report.Code, server.WipeCutoff)
				if err != nil {
					logger.Error("error fetching report details", "report", report.Code)
					continue
				}
				logger.Info("loaded report details", "report", report.Code, "duration", time.Since(start).Truncate(time.Millisecond))
				logger.Info("report has changes, sending updates", "report", report.Code)
				w.sendUpdate(ctx, server, !isOutdated, report, details)
				lr := CachedReport{code: report.Code, endTime: report.EndTime, isLive: !isOutdated}
				reportsCache.Set(report.Code, lr, ttlcache.DefaultTTL)
			case cachedReport.isLive && isOutdated:
				start := time.Now()
				details, err := w.wlClient.TopDeathsForReport(ctx, report.Code, server.WipeCutoff)
				if err != nil {
					logger.Error("error fetching report details", "report", report.Code)
					continue
				}
				logger.Info("loaded report details", "report", report.Code, "duration", time.Since(start).Truncate(time.Millisecond))
				logger.Info("report went offline, sending updates", "report", report.Code)
				w.sendUpdate(ctx, server, false, report, details)
				lr := CachedReport{code: report.Code, endTime: report.EndTime, isLive: false}
				reportsCache.Set(report.Code, lr, ttlcache.DefaultTTL)
			default:
				logger.Info("report has no changes, skipping", "report", report.Code)
			}
		}
	}
}

func (w *Watcher) sendUpdate(ctx context.Context, server storage.Server, isLive bool, report warcraftlogs.Report, details warcraftlogs.ReportDetails) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	w.handler(StatsEvent{
		Server:        server,
		ReportId:      report.Code,
		Title:         report.Title,
		Zone:          report.Zone.Name,
		URL:           fmt.Sprintf("https://www.warcraftlogs.com/reports/%v", report.Code),
		Live:          isLive,
		TopDeath:      details.TopDeaths,
		TopFirstDeath: details.TopFirstDeaths,
		StartedBy:     report.Owner.Name,
		StartedAt:     time.UnixMilli(report.StartTime),
		LastUpload:    time.UnixMilli(report.EndTime),
	})
}

func deleteNonRaid(reports []warcraftlogs.Report) []warcraftlogs.Report {
	return slices.DeleteFunc(reports, func(report warcraftlogs.Report) bool {
		for _, difficulty := range report.Zone.Difficulties {
			if difficulty.Name == "Mythic" && len(difficulty.Sizes) == 1 && difficulty.Sizes[0] == 20 {
				return false
			}
		}
		return true
	})
}

func (w *Watcher) Unwatch(serverId string) {
	cancel, isKnown := w.watched.LoadAndDelete(serverId)
	if isKnown {
		cancel.(context.CancelFunc)()
	}
}

func (w *Watcher) OnUpdate(handler func(se StatsEvent)) {
	w.handler = handler
}
