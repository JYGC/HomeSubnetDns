// Package migrations defines the HomeSubnetDns PocketBase schema and settings.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

const (
	hostnamePattern = `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	ipv4Pattern     = `^((25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])$`

	// Only the dnssync service account may read hosts; superusers bypass rules
	// and are the only ones who can create, update, or delete.
	syncReadRule = `@request.auth.collectionName = "service_accounts"`
)

func init() {
	m.Register(func(app core.App) error {
		// Service accounts: no API access rules at all (superuser-managed).
		accounts := core.NewAuthCollection("service_accounts")
		if err := app.Save(accounts); err != nil {
			return err
		}

		hosts := core.NewBaseCollection("hosts")
		hosts.ListRule = types.Pointer(syncReadRule)
		hosts.ViewRule = types.Pointer(syncReadRule)
		hosts.Fields.Add(
			&core.TextField{
				Name:        "name",
				Required:    true,
				Max:         63,
				Pattern:     hostnamePattern,
				Presentable: true,
				Help:        "Host label; served as <name>.home.arpa. Lowercase letters, digits and hyphens.",
			},
			&core.TextField{
				Name:     "ipv4",
				Required: true,
				Pattern:  ipv4Pattern,
				Help:     "IPv4 address, e.g. 192.0.2.20.",
			},
			&core.BoolField{
				Name: "no_ptr",
				Help: "Tick to skip the reverse (PTR) record, e.g. for an extra name on an IP that already has one.",
			},
			&core.BoolField{
				Name: "disabled",
				Help: "Tick to stop serving this record without deleting it.",
			},
			&core.TextField{
				Name: "notes",
				Max:  500,
			},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		hosts.AddIndex("idx_hosts_name", true, "`name`", "")
		// One reverse name per active IP.
		hosts.AddIndex("idx_hosts_ptr_ipv4", true, "`ipv4`", "`no_ptr` = FALSE AND `disabled` = FALSE")
		if err := app.Save(hosts); err != nil {
			return err
		}

		settings := app.Settings()
		settings.Meta.AppName = "HomeSubnetDns"
		// AppURL follows the address PocketBase serves on (main.go, D41).
		settings.Backups.Cron = "0 3 * * *"
		settings.Backups.CronMaxKeep = 7
		settings.RateLimits.Enabled = true
		return app.Save(settings)
	}, func(app core.App) error {
		for _, name := range []string{"hosts", "service_accounts"} {
			col, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				continue
			}
			if err := app.Delete(col); err != nil {
				return err
			}
		}
		return nil
	})
}
