package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

func MigrateLegacyRecords(ctx context.Context, tx *sql.Tx, box *secrets.Box, postgres bool) error {
	query := func(text string) string {
		if !postgres {
			return text
		}
		index := 0
		for strings.Contains(text, "?") {
			index++
			text = strings.Replace(text, "?", fmt.Sprintf("$%d", index), 1)
		}
		return text
	}
	hasTable := func(name string) (bool, error) {
		var found bool
		statement := `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`
		if postgres {
			statement = `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=?)`
		}
		err := tx.QueryRowContext(ctx, query(statement), name).Scan(&found)
		return found, err
	}
	hasColumn := func(table, column string) (bool, error) {
		var found bool
		statement := `SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name=?)`
		if postgres {
			statement = `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=? AND column_name=?)`
		}
		err := tx.QueryRowContext(ctx, query(statement), table, column).Scan(&found)
		return found, err
	}
	connections := map[string]string{}
	legacy, err := hasTable("cloudflare_connections")
	if err != nil {
		return err
	}
	if legacy {
		rows, err := tx.QueryContext(ctx, `SELECT org_id,name,account_id,tunnel_id,tunnel_token_enc,api_token_enc,access_app_id,origin,connector,metrics_addr,prune,updated_at FROM cloudflare_connections ORDER BY org_id`)
		if err != nil {
			return err
		}
		type row struct {
			org, name, account, tunnel, access, origin, connector, metrics string
			tunnelCipher, apiCipher                                        []byte
			prune                                                          bool
			updated                                                        int64
		}
		items := []row{}
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.org, &item.name, &item.account, &item.tunnel, &item.tunnelCipher, &item.apiCipher, &item.access, &item.origin, &item.connector, &item.metrics, &item.prune, &item.updated); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range items {
			id := NewID("conn")
			secretValues := map[string]string{}
			for kind, cipher := range map[string][]byte{"tunnel_token": item.tunnelCipher, "api_token": item.apiCipher} {
				if len(cipher) == 0 {
					continue
				}
				if box == nil {
					return errors.New("matching secret key is required for encrypted legacy connections")
				}
				plain, err := box.Open(cipher, "cloudflare:"+item.org+":"+kind)
				if err != nil {
					return errors.New("legacy connection secrets cannot be decrypted with the supplied key")
				}
				secretValues[kind] = string(plain)
			}
			var encrypted []byte
			if len(secretValues) > 0 {
				plain, err := json.Marshal(secretValues)
				if err != nil {
					return err
				}
				encrypted, err = box.Seal(plain, "connection:"+item.org+":"+id)
				if err != nil {
					return err
				}
			}
			config, err := json.Marshal(map[string]any{"account_id": item.account, "tunnel_id": item.tunnel, "access_app_id": item.access, "origin": item.origin, "connector": item.connector, "metrics_addr": item.metrics, "prune": item.prune})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, query(`INSERT INTO connections(id,org_id,type,name,config,secrets_enc,created_at,updated_at) VALUES(?,?,'cloudflare',?,?,?,?,?)`), id, item.org, item.name, string(config), encrypted, item.updated, item.updated); err != nil {
				return errors.New("legacy connection conflicts with an existing connection")
			}
			connections[item.org] = id
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,org_id,kind,domain,policy FROM endpoints WHERE policy IS NOT NULL AND policy<>''`)
	if err != nil {
		return err
	}
	type endpoint struct {
		id, org, kind string
		domain        sql.NullString
		policy        string
	}
	endpoints := []endpoint{}
	for rows.Next() {
		var ep endpoint
		if err := rows.Scan(&ep.id, &ep.org, &ep.kind, &ep.domain, &ep.policy); err != nil {
			rows.Close()
			return err
		}
		endpoints = append(endpoints, ep)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ep := range endpoints {
		var policy map[string]json.RawMessage
		if err := validateLegacyDocument([]byte(ep.policy)); err != nil {
			return errors.New("invalid saved endpoint policy")
		}
		if err := json.Unmarshal([]byte(ep.policy), &policy); err != nil || policy == nil {
			return errors.New("invalid saved endpoint policy")
		}
		old, exists := policy["public_access"]
		if !exists {
			continue
		}
		var public string
		if err := json.Unmarshal(old, &public); err != nil {
			return errors.New("invalid saved public access")
		}
		delete(policy, "public_access")
		if public != "" {
			if public != "cloudflare" {
				return errors.New("unknown saved public access provider")
			}
			if ep.kind == "" || ep.kind == KindHTTP {
				id := connections[ep.org]
				if id == "" {
					return errors.New("saved public access has no matching legacy connection")
				}
				var refs []AccessRef
				if current := policy["access"]; len(current) > 0 {
					if err := json.Unmarshal(current, &refs); err != nil {
						return errors.New("invalid saved access references")
					}
				}
				for _, ref := range refs {
					if ref.ConnectionID == id {
						return errors.New("duplicate migrated access reference")
					}
				}
				refs = append(refs, AccessRef{ConnectionID: id, Hostname: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ep.domain.String)), ".")})
				encoded, err := json.Marshal(refs)
				if err != nil {
					return err
				}
				policy["access"] = encoded
			}
		}
		encoded, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, query(`UPDATE endpoints SET policy=? WHERE id=?`), string(encoded), ep.id); err != nil {
			return err
		}
	}
	spec, err := hasColumn("services", "spec")
	if err != nil {
		return err
	}
	if spec {
		rows, err := tx.QueryContext(ctx, `SELECT id,driver,spec,config FROM services ORDER BY id`)
		if err != nil {
			return err
		}
		type service struct {
			id, driver, spec string
			config           sql.NullString
		}
		items := []service{}
		for rows.Next() {
			var item service
			if err := rows.Scan(&item.id, &item.driver, &item.spec, &item.config); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.config.Valid {
				continue
			}
			driver, config, ready, err := MigrateService(item.driver, json.RawMessage(item.spec))
			if err != nil {
				return fmt.Errorf("convert legacy service: %w", err)
			}
			if _, err := tx.ExecContext(ctx, query(`UPDATE services SET driver=?,config=?,ready=? WHERE id=?`), driver, string(config), string(ready), item.id); err != nil {
				return err
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT org_id,cedar_src FROM org_policies`)
	if err != nil {
		return err
	}
	type policy struct{ org, source string }
	policies := []policy{}
	for rows.Next() {
		var item policy
		if err := rows.Scan(&item.org, &item.source); err != nil {
			rows.Close()
			return err
		}
		policies = append(policies, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range policies {
		converted, err := authz.MigrateLegacyPolicy(item.source)
		if err != nil {
			return err
		}
		if converted != item.source {
			if _, err := tx.ExecContext(ctx, query(`UPDATE org_policies SET cedar_src=? WHERE org_id=?`), converted, item.org); err != nil {
				return err
			}
		}
	}
	if box != nil {
		rows, err := tx.QueryContext(ctx, `SELECT id,org_id,secrets_enc FROM connections WHERE secrets_enc IS NOT NULL`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, org string
			var cipher []byte
			if err := rows.Scan(&id, &org, &cipher); err != nil {
				rows.Close()
				return err
			}
			if len(cipher) == 0 {
				continue
			}
			plain, err := box.Open(cipher, "connection:"+org+":"+id)
			if err != nil {
				rows.Close()
				return errors.New("connection secrets cannot be decrypted with the supplied key")
			}
			var secretValues map[string]string
			if err := validateLegacyDocument(plain); err != nil {
				rows.Close()
				return errors.New("invalid saved connection secrets")
			}
			if err := json.Unmarshal(plain, &secretValues); err != nil || secretValues == nil {
				rows.Close()
				return errors.New("invalid saved connection secrets")
			}
			fields := make([]typedfields.Field, 0, len(secretValues))
			for key := range secretValues {
				fields = append(fields, typedfields.Field{Key: key, Kind: "secret"})
			}
			if err := typedfields.Validate(fields, plain); err != nil {
				rows.Close()
				return errors.New("invalid saved connection secrets")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if spec {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE services DROP COLUMN spec`); err != nil {
			return err
		}
	}
	if legacy {
		if _, err := tx.ExecContext(ctx, `DROP TABLE cloudflare_connections`); err != nil {
			return err
		}
	}
	return nil
}

func validateLegacyDocument(raw []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := validateUniqueJSON(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("legacy JSON has trailing data")
	}
	return nil
}
