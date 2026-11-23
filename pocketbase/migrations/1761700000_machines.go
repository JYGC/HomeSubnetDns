package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Machines on the LAN (PLAN.md D44): what the LAN scan (lanscan.go) finds, one
// record per MAC, kept until 30 days after the machine was last seen. The
// online_machines view lists the ones that answered the latest scan.
const macPattern = `^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`

func init() {
	m.Register(func(app core.App) error {
		// No API rules, so only superusers see them, like devices: it's a list of
		// the household's machines and when they're on.
		machines := core.NewBaseCollection("machines")
		machines.Fields.Add(
			&core.TextField{
				Name:        "mac",
				Required:    true,
				Pattern:     macPattern,
				Presentable: true,
				Help:        "The machine's Ethernet or Wi-Fi address. A phone with a private Wi-Fi address gets a new record when it changes.",
			},
			&core.TextField{
				Name:    "ipv4",
				Pattern: ipv4Pattern,
				Help:    "The address it answered on when last seen.",
			},
			&core.NumberField{
				Name:    "ip_order",
				OnlyInt: true,
				Help:    "ipv4 as a number: sort by this to list machines by address.",
			},
			&core.BoolField{
				Name: "online",
				Help: "Answered the latest scan (every 2 minutes).",
			},
			&core.TextField{
				Name: "dns_name",
				Max:  253,
				Help: "This box's reverse (PTR) name for ipv4, e.g. nas.<domain>, when last seen.",
			},
			&core.TextField{
				Name: "hostname",
				Max:  255,
				Help: "The name it gave when it got its address from this box's DHCP server.",
			},
			&core.DateField{
				Name: "first_seen",
			},
			&core.DateField{
				Name: "last_seen",
				Help: "The record is deleted 30 days after this.",
			},
			&core.TextField{
				Name: "notes",
				Max:  500,
				Help: "Yours, e.g. what the machine is; scans never change it.",
			},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		machines.AddIndex("idx_machines_mac", true, "`mac`", "")
		if err := app.Save(machines); err != nil {
			return err
		}

		online := core.NewViewCollection("online_machines")
		online.ViewQuery = "SELECT id, ipv4, dns_name, hostname, mac, last_seen, first_seen, notes, ip_order " +
			"FROM machines WHERE online = TRUE ORDER BY ip_order"
		return app.Save(online)
	}, func(app core.App) error {
		for _, name := range []string{"online_machines", "machines"} {
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
