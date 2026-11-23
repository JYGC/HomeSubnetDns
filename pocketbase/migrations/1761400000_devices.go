package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Device check-ins (PLAN.md §9): a "devices" auth collection, and hosts records
// a device owns through the "device" relation.
func init() {
	m.Register(func(app core.App) error {
		// Superuser-managed with no API rules. Devices authenticate only through
		// GET /api/hostupdate (main.go), so the standard auth endpoints stay off.
		devices := core.NewAuthCollection("devices")
		devices.PasswordAuth.Enabled = false
		devices.AuthAlert.Enabled = false
		devices.Fields.GetByName(core.FieldNameEmail).(*core.EmailField).Required = false
		devices.Fields.GetByName(core.FieldNamePassword).(*core.PasswordField).Min = 32
		devices.Fields.Add(
			&core.TextField{
				Name:        "name",
				Required:    true,
				Max:         63,
				Pattern:     hostnamePattern,
				Presentable: true,
				Help:        "The one name this device may update, served as <name>.home.arpa.",
			},
			&core.BoolField{
				Name: "disabled",
				Help: "Tick to refuse this device's check-ins. Delete the device to also delete its host record.",
			},
			&core.TextField{
				Name: "notes",
				Max:  500,
			},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		devices.AddIndex("idx_devices_name", true, "`name`", "")
		if err := app.Save(devices); err != nil {
			return err
		}

		hosts, err := app.FindCollectionByNameOrId("hosts")
		if err != nil {
			return err
		}
		// A check-in releases an IP from another device's record by clearing it;
		// main.go still requires ipv4 on records without a device.
		ipv4 := hosts.Fields.GetByName("ipv4").(*core.TextField)
		ipv4.Required = false
		ipv4.Help = "IPv4 address, e.g. 192.0.2.20. Set by check-ins on device records."
		hosts.Fields.AddAt(3, // after ipv4
			&core.RelationField{
				Name:          "device",
				CollectionId:  devices.Id,
				MaxSelect:     1,
				CascadeDelete: true,
				Help:          "Set when a device owns this record: its check-ins update ipv4, and it stops being served 24 h after the last one.",
			},
			&core.DateField{
				Name: "last_seen",
				Help: "Time of the owning device's last check-in.",
			},
		)
		hosts.RemoveIndex("idx_hosts_ptr_ipv4")
		hosts.AddIndex("idx_hosts_ptr_ipv4", true, "`ipv4`", "`no_ptr` = FALSE AND `disabled` = FALSE AND `ipv4` != ''")
		hosts.AddIndex("idx_hosts_device", true, "`device`", "`device` != ''")
		return app.Save(hosts)
	}, func(app core.App) error {
		hosts, err := app.FindCollectionByNameOrId("hosts")
		if err != nil {
			return err
		}
		hosts.Fields.RemoveByName("device")
		hosts.Fields.RemoveByName("last_seen")
		hosts.Fields.GetByName("ipv4").(*core.TextField).Required = true
		hosts.RemoveIndex("idx_hosts_device")
		hosts.RemoveIndex("idx_hosts_ptr_ipv4")
		hosts.AddIndex("idx_hosts_ptr_ipv4", true, "`ipv4`", "`no_ptr` = FALSE AND `disabled` = FALSE")
		if err := app.Save(hosts); err != nil {
			return err
		}

		devices, err := app.FindCollectionByNameOrId("devices")
		if err != nil {
			return nil
		}
		return app.Delete(devices)
	})
}
