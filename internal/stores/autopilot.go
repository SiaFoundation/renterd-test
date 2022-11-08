package stores

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.sia.tech/renterd/autopilot"
	"go.sia.tech/renterd/internal/consensus"
	"go.sia.tech/siad/modules"
)

// EphemeralAutopilotStore implements autopilot.Store in memory.
type EphemeralAutopilotStore struct {
	mu     sync.Mutex
	tip    consensus.ChainIndex
	config autopilot.Config
}

// Tip implements autopilot.Store.
func (s *EphemeralAutopilotStore) Tip() consensus.ChainIndex {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tip
}

// Config implements autopilot.Store.
func (s *EphemeralAutopilotStore) Config() autopilot.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

// SetConfig implements autopilot.Store.
func (s *EphemeralAutopilotStore) SetConfig(c autopilot.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = c
	return nil
}

// ProcessConsensusChange implements chain.Subscriber.
func (s *EphemeralAutopilotStore) ProcessConsensusChange(cc modules.ConsensusChange) {
	panic("unimplemented")
}

// NewEphemeralAutopilotStore returns a new EphemeralAutopilotStore.
func NewEphemeralAutopilotStore() *EphemeralAutopilotStore {
	return &EphemeralAutopilotStore{}
}

// JSONAutopilotStore implements autopilot.Store in memory, backed by a JSON file.
type JSONAutopilotStore struct {
	*EphemeralAutopilotStore
	dir      string
	lastSave time.Time
}

type jsonAutopilotPersistData struct {
	Config autopilot.Config
}

func (s *JSONAutopilotStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p jsonAutopilotPersistData
	p.Config = s.config
	js, _ := json.MarshalIndent(p, "", "  ")

	// atomic save
	dst := filepath.Join(s.dir, "autopilot.json")
	f, err := os.OpenFile(dst+"_tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0660)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(js); err != nil {
		return err
	} else if err := f.Sync(); err != nil {
		return err
	} else if err := f.Close(); err != nil {
		return err
	} else if err := os.Rename(dst+"_tmp", dst); err != nil {
		return err
	}
	return nil
}

func (s *JSONAutopilotStore) load() error {
	var p jsonAutopilotPersistData
	if js, err := os.ReadFile(filepath.Join(s.dir, "autopilot.json")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	} else if err := json.Unmarshal(js, &p); err != nil {
		return err
	}
	s.config = p.Config
	return nil
}

// SetConfig implements autopilot.Store.
func (s *JSONAutopilotStore) SetConfig(c autopilot.Config) error {
	s.EphemeralAutopilotStore.SetConfig(c)
	return s.save()
}

// ProcessConsensusChange implements chain.Subscriber.
func (s *JSONAutopilotStore) ProcessConsensusChange(cc modules.ConsensusChange) {
	s.EphemeralAutopilotStore.ProcessConsensusChange(cc)
	if time.Since(s.lastSave) > 2*time.Minute {
		if err := s.save(); err != nil {
			log.Fatalln("Couldn't save autopilot state:", err)
		}
		s.lastSave = time.Now()
	}
}

// NewJSONAutopilotStore returns a new JSONAutopilotStore.
func NewJSONAutopilotStore(dir string) (*JSONAutopilotStore, error) {
	s := &JSONAutopilotStore{
		EphemeralAutopilotStore: NewEphemeralAutopilotStore(),
		dir:                     dir,
		lastSave:                time.Now(),
	}
	err := s.load()
	if err != nil {
		return nil, err
	}
	return s, nil
}