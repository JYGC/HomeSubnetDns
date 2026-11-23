// PocketBase build for HomeSubnetDns: stores host records, served as
// <name>.<domain>, that dnssync renders into an Unbound include file, and
// lists the machines on the LAN (lanscan.go).
package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/ozzo-validation/v4/is"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"

	_ "homesubnetdns/pocketbase/migrations"
)

// reservedNames are served from unbound/static.conf; keep the two in sync.
var reservedNames = map[string]bool{
	"dns":        true,
	"pocketbase": true,
	"router":     true,
}

// domain is the zone names are served in (site.conf DOMAIN), from --dns-domain.
var domain string

const labelPattern = `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`

var domainRe = regexp.MustCompile(`^` + labelPattern + `(\.` + labelPattern + `)*$`)

// maxNameLength is DNS's limit for a whole name, e.g. runtask.test.app.home.arpa.
const maxNameLength = 253

func main() {
	app := pocketbase.New()
	var lan, gateway string
	flags := app.RootCmd.PersistentFlags()
	flags.StringVar(&domain, "dns-domain", "",
		"the zone hosts names are served in, e.g. home.arpa (site.conf DOMAIN)")
	flags.StringVar(&lan, "lan", "",
		"this box's address and the LAN's prefix length, e.g. 192.0.2.53/24 (D41)")
	flags.StringVar(&gateway, "gateway", "", "the LAN's router, e.g. 192.0.2.1")
	_ = app.RootCmd.ParseFlags(os.Args[1:])

	registerHooks(app)
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		if !domainRe.MatchString(domain) || len(domain) > maxNameLength-2 {
			return errors.New("--dns-domain must be the zone names are served in, e.g. --dns-domain=home.arpa")
		}
		if err := setLAN(lan, gateway); err != nil {
			return err
		}
		scheduleLANScan(se.App)
		// Links PocketBase makes use the address it serves on, which moves with the box.
		if url := "http://" + se.Server.Addr; se.App.Settings().Meta.AppURL != url {
			settings := se.App.Settings()
			settings.Meta.AppURL = url
			if err := se.App.Save(settings); err != nil {
				se.App.Logger().Warn("can't set the application URL", "url", url, "error", err)
			}
		}
		se.Router.GET("/api/hostupdate", hostUpdateHandler)
		se.Router.GET("/api/hostupdate/info", hostUpdateInfoHandler)
		return se.Next()
	})

	app.RootCmd.AddCommand(serviceAccountCommand(app))
	app.RootCmd.AddCommand(lanScanCommand(app, &lan, &gateway))

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

func registerHooks(app core.App) {
	app.OnRecordValidate("hosts").BindFunc(func(e *core.RecordEvent) error {
		name := e.Record.GetString("name")
		// Also refuses names under a reserved one, e.g. www.router.
		if reservedNames[name[strings.LastIndex(name, ".")+1:]] {
			return reservedNameError()
		}
		if domain != "" && len(name)+1+len(domain) > maxNameLength {
			return validation.Errors{
				"name": validation.NewError("validation_name_too_long",
					fmt.Sprintf("Too long: %s.%s would exceed DNS's %d characters.", name, domain, maxNameLength)),
			}
		}
		if e.Record.GetString("alias_of") != "" {
			if err := validateAlias(e.App, e.Record); err != nil {
				return err
			}
		} else if e.Record.GetBool("takes_ptr") {
			return validation.Errors{
				"takes_ptr": validation.NewError("validation_alias", "Only an alias can take a reverse (PTR) record: set alias_of, or untick this."),
			}
		} else if e.Record.GetString("device") == "" && e.Record.GetString("ipv4") == "" {
			// ipv4 is optional in the schema so check-ins can release it, and aliases have none.
			return validation.Errors{
				"ipv4": validation.NewError("validation_required", "Required, unless a device owns the record or alias_of is set."),
			}
		}
		return e.Next()
	})

	app.OnRecordValidate("devices").BindFunc(func(e *core.RecordEvent) error {
		if reservedNames[e.Record.GetString("name")] {
			return reservedNameError()
		}
		return e.Next()
	})
}

// validateAlias checks a record with alias_of set (D33): it takes its address
// from a target that holds one itself, so aliases never chain. With takes_ptr
// it also takes the target's PTR, one alias per target (D36).
func validateAlias(app core.App, r *core.Record) error {
	// The API capitalises each message's first letter, so none starts with a name.
	aliasErr := func(field, msg string) error {
		return validation.Errors{field: validation.NewError("validation_alias", msg)}
	}
	if r.GetString("device") != "" {
		return aliasErr("alias_of", "A device's record can't be an alias.")
	}
	if r.GetString("ipv4") != "" {
		return aliasErr("ipv4", "Leave empty on an alias: it's served with its alias_of record's address.")
	}
	target := r.GetString("alias_of")
	if target == r.Id {
		return aliasErr("alias_of", "An alias can't point at itself.")
	}
	t, err := app.FindRecordById("hosts", target)
	if err != nil {
		return aliasErr("alias_of", "No such hosts record.")
	}
	if t.GetString("alias_of") != "" {
		return aliasErr("alias_of", fmt.Sprintf("That's an alias itself (%s); point at the record it follows instead.", t.GetString("name")))
	}
	if !r.IsNew() {
		if a, err := app.FindFirstRecordByData("hosts", "alias_of", r.Id); err == nil {
			return aliasErr("alias_of", fmt.Sprintf("This record has an alias (%s), and aliases can't chain.", a.GetString("name")))
		}
	}
	if r.GetBool("takes_ptr") {
		// D36: the target's PTR gives this name. If no_ptr is ticked on the
		// target later, that wins and there is no PTR.
		if t.GetBool("no_ptr") {
			return aliasErr("takes_ptr", fmt.Sprintf("Nothing to take: %s has no_ptr ticked, so it has no reverse (PTR) record.", t.GetString("name")))
		}
		if a, err := app.FindFirstRecordByFilter("hosts", "alias_of = {:target} && takes_ptr = true && id != {:id}",
			dbx.Params{"target": target, "id": r.Id}); err == nil {
			return aliasErr("takes_ptr", fmt.Sprintf("Already taken: %s takes %s's reverse (PTR) record; untick it there first.", a.GetString("name"), t.GetString("name")))
		}
	}
	return nil
}

func reservedNameError() error {
	return validation.Errors{
		"name": validation.NewError("validation_reserved_name", "This name is reserved (defined in static.conf)."),
	}
}

// serviceAccountCommand creates or updates a service_accounts record, reading
// the password from stdin so it never appears in the process list.
func serviceAccountCommand(app core.App) *cobra.Command {
	return &cobra.Command{
		Use:          "service-account EMAIL",
		Example:      "service-account dnssync@dns.invalid < passfile",
		Short:        "Creates, or updates if email exists, a service account (password on stdin)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(command *cobra.Command, args []string) error {
			email := args[0]
			if is.EmailFormat.Validate(email) != nil {
				return errors.New("invalid email address")
			}

			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
			if err != nil {
				return fmt.Errorf("failed to read password from stdin: %w", err)
			}
			password := strings.TrimSpace(string(raw))
			if len(password) < 16 {
				return errors.New("password must be at least 16 characters")
			}

			if err := app.RunAllMigrations(); err != nil {
				return fmt.Errorf("failed to apply migrations: %w", err)
			}

			col, err := app.FindCollectionByNameOrId("service_accounts")
			if err != nil {
				return fmt.Errorf("failed to fetch service_accounts collection: %w", err)
			}

			record, err := app.FindAuthRecordByEmail(col, email)
			if err != nil {
				record = core.NewRecord(col)
			}
			record.SetEmail(email)
			record.SetPassword(password)
			record.SetVerified(true)

			if err := app.Save(record); err != nil {
				return fmt.Errorf("failed to save service account: %w", err)
			}

			fmt.Printf("Saved service account %q\n", email)
			return nil
		},
	}
}

// lanScanCommand scans the LAN once, saves the scan as the scheduled one does,
// and lists the machines online (jobs/25-lan-scan.sh, the installer's checks).
func lanScanCommand(app core.App, lan, gateway *string) *cobra.Command {
	return &cobra.Command{
		Use:          "lan-scan",
		Example:      "lan-scan --dir pb_data --lan=192.0.2.53/24 --gateway=192.0.2.1",
		Short:        "Scans the LAN once, saves it in machines, and lists the machines online",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, args []string) error {
			if err := setLAN(*lan, *gateway); err != nil {
				return err
			}
			if err := app.RunAllMigrations(); err != nil {
				return fmt.Errorf("failed to apply migrations: %w", err)
			}
			res, err := lanScan(app)
			if err != nil {
				return err
			}
			online, err := app.FindRecordsByFilter("machines", "online = true", "ip_order", 0, 0)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(command.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ADDRESS\tMAC\tDNS NAME\tDHCP HOSTNAME")
			for _, r := range online {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.GetString("ipv4"), r.GetString("mac"),
					orDash(r.GetString("dns_name")), orDash(r.GetString("hostname")))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "%d machines online on %s: %d new, %d went offline\n",
				res.online, lanPrefix, res.new, res.offline)
			return nil
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
