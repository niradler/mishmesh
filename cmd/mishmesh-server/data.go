package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/postgres"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func dataPool(cfg config.Server) postgres.PoolConfig {
	return postgres.PoolConfig{MaxOpenConns: cfg.DataMaxConns, MaxIdleConns: cfg.DataMaxIdleConns, ConnMaxLifetime: cfg.DataConnMaxLifetime, ConnMaxIdleTime: cfg.DataConnMaxIdleTime, QueryTimeout: cfg.DataQueryTimeout}
}

func loadDataBox(cfg config.Server) (*secrets.Box, error) {
	keyFile := cfg.EffectiveSecretKeyFile()
	if _, err := os.Stat(keyFile); errors.Is(err, os.ErrNotExist) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var encrypted bool
		switch cfg.EffectiveDataBackend() {
		case "sqlite":
			encrypted, err = sqlite.HasEncryptedConnections(ctx, cfg.DataDSN)
		case "postgres":
			encrypted, err = postgres.HasEncryptedConnections(ctx, cfg.DataDSN, dataPool(cfg))
		default:
			return nil, fmt.Errorf("unknown DATA_BACKEND %q (want sqlite or postgres)", cfg.EffectiveDataBackend())
		}
		if err != nil {
			return nil, fmt.Errorf("check encrypted connection data before key creation: %w", err)
		}
		if encrypted {
			return nil, errors.New("connection ciphertext exists but its secret key is missing; restore the matching key or set MISHMESH_SECRET_KEY_FILE before opening this database")
		}
	} else if err != nil {
		return nil, err
	}
	key, err := secrets.LoadOrCreateKey(keyFile)
	if err != nil {
		return nil, err
	}
	return secrets.New(key)
}

func openDataStoreWithBox(cfg config.Server) (store.DataStore, *secrets.Box, error) {
	box, err := loadDataBox(cfg)
	if err != nil {
		return nil, nil, err
	}
	var data store.DataStore
	switch cfg.EffectiveDataBackend() {
	case "sqlite":
		data, err = sqlite.Open(cfg.DataDSN)
	case "postgres":
		data, err = postgres.Open(cfg.DataDSN, dataPool(cfg))
	default:
		err = fmt.Errorf("unknown DATA_BACKEND %q (want sqlite or postgres)", cfg.EffectiveDataBackend())
	}
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	orgs, err := data.ListOrgs(ctx)
	if err == nil {
		for _, org := range orgs {
			var records []*store.Connection
			records, err = data.ListConnections(ctx, org.ID)
			if err != nil {
				break
			}
			for _, record := range records {
				_, err = connections.OpenConfig(*record, box, cfg.HostCommands)
				if err != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		_ = data.Close()
		return nil, nil, err
	}
	return data, box, nil
}
