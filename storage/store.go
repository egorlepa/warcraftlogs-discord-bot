package storage

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

var serversBucket = []byte("servers")

type Store struct {
	db *bolt.DB
}

func New(db *bolt.DB) *Store {
	return &Store{db: db}
}

func MustInitDB(db *bolt.DB) {
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("servers"))
		return err
	})
	if err != nil {
		panic(err)
	}
}

type Server struct {
	ServerId   string `json:"server_id"`
	ChannelId  string `json:"channel_id"`
	WlGuildId  int64  `json:"wl_guild_id"`
	WipeCutoff int64  `json:"wipe_cutoff"`
}

func (s *Store) SaveServer(server Server) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(serversBucket)
		data, _ := json.Marshal(&server)
		return b.Put([]byte(server.ServerId), data)
	})
}

func (s *Store) ReadServer(serverId string) (*Server, error) {
	var server *Server
	_ = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(serversBucket)
		data := b.Get([]byte(serverId))
		if len(data) == 0 {
			return nil
		}
		var srv Server
		_ = json.Unmarshal(data, &srv)
		server = &srv
		return nil
	})
	return server, nil
}

func (s *Store) DeleteServer(serverId string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(serversBucket)
		return b.Delete([]byte(serverId))
	})
}
