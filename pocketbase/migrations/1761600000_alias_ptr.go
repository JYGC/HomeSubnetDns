package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// An alias can take its target's reverse (PTR) record (PLAN.md D36), so that
// address lookups give the name the machine is known by, e.g. runtask.test.app
// rather than its device name.
func init() {
	m.Register(func(app core.App) error {
		hosts, err := app.FindCollectionByNameOrId("hosts")
		if err != nil {
			return err
		}
		hosts.Fields.AddAt(4, // after alias_of
			&core.BoolField{
				Name: "takes_ptr",
				Help: "On an alias: tick so the reverse (PTR) record of its alias_of record's address gives this name instead. One alias per record.",
			},
		)
		hosts.Fields.GetByName("no_ptr").(*core.BoolField).Help =
			"Tick to skip the reverse (PTR) record, e.g. for an extra name on an IP that already has one. Aliases get one only through takes_ptr."
		// One per target; main.go explains a clash before this index sees it.
		hosts.AddIndex("idx_hosts_takes_ptr", true, "`alias_of`", "`takes_ptr` = TRUE AND `alias_of` != ''")
		return app.Save(hosts)
	}, func(app core.App) error {
		hosts, err := app.FindCollectionByNameOrId("hosts")
		if err != nil {
			return err
		}
		hosts.RemoveIndex("idx_hosts_takes_ptr")
		hosts.Fields.RemoveByName("takes_ptr")
		hosts.Fields.GetByName("no_ptr").(*core.BoolField).Help =
			"Tick to skip the reverse (PTR) record, e.g. for an extra name on an IP that already has one. Aliases never get one."
		return app.Save(hosts)
	})
}
