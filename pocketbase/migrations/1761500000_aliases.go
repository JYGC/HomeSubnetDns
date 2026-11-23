package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Dotted names and aliases (PLAN.md D30, D33-D35): hosts names may have several
// labels, e.g. runtask.test.app, and a record can follow another record's
// address through "alias_of" instead of holding one.
const dottedNamePattern = `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`

func init() {
	m.Register(func(app core.App) error {
		hosts, err := app.FindCollectionByNameOrId("hosts")
		if err != nil {
			return err
		}
		// main.go also checks that <name>.<domain> fits DNS's 253 characters.
		name := hosts.Fields.GetByName("name").(*core.TextField)
		name.Max = 253
		name.Pattern = dottedNamePattern
		name.Help = "Served as <name>.<domain>, e.g. nas or runtask.test.app. Lowercase letters, digits and hyphens, with dots between parts."
		hosts.Fields.GetByName("ipv4").(*core.TextField).Help =
			"IPv4 address, e.g. 192.0.2.20. Set by check-ins on device records; empty on aliases."
		hosts.Fields.GetByName("no_ptr").(*core.BoolField).Help =
			"Tick to skip the reverse (PTR) record, e.g. for an extra name on an IP that already has one. Aliases never get one."
		// Not a cascade: deleting the target empties alias_of, and the alias
		// stops being served until it is pointed elsewhere (D34).
		hosts.Fields.AddAt(3, // after ipv4
			&core.RelationField{
				Name:         "alias_of",
				CollectionId: hosts.Id,
				MaxSelect:    1,
				Help:         "Makes this record an alias: served with that record's address, while that record is served. Leave ipv4 empty.",
			},
		)
		if err := app.Save(hosts); err != nil {
			return err
		}

		devices, err := app.FindCollectionByNameOrId("devices")
		if err != nil {
			return err
		}
		devices.Fields.GetByName("name").(*core.TextField).Help =
			"The one name this device may update, served as <name>.<domain>. One part, no dots; add aliases in hosts for more names."
		return app.Save(devices)
	}, func(app core.App) error {
		aliases, err := app.FindRecordsByFilter("hosts", "alias_of != ''", "", 0, 0)
		if err != nil {
			return err
		}
		for _, r := range aliases {
			if err := app.Delete(r); err != nil {
				return err
			}
		}

		hosts, err := app.FindCollectionByNameOrId("hosts")
		if err != nil {
			return err
		}
		hosts.Fields.RemoveByName("alias_of")
		name := hosts.Fields.GetByName("name").(*core.TextField)
		name.Max = 63
		name.Pattern = hostnamePattern
		return app.Save(hosts)
	})
}
