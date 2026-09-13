package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"mime"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"as215520.net/forge/internal/config"
	"as215520.net/forge/internal/forge"
	"as215520.net/forge/internal/mirror"
	"as215520.net/forge/internal/repl"
	"as215520.net/forge/internal/store"
	"as215520.net/forge/internal/version"
)

func adminUsage() {
	fmt.Fprint(os.Stderr, `usage: forge admin [--config FILE] <command>

  init         --data DIR --hostname NAME [--node NAME] [--write-config FILE]
  status
  user list | user create NAME [--admin] | user disable NAME | user enable NAME | user admin NAME [--revoke]
  key add USER FILE | key list USER | key revoke FINGERPRINT
  cert list USER | cert revoke SPKI | cert enrol-code USER
  repo list | repo create OWNER/NAME [--private] [--description TEXT]
  repo delete OWNER/NAME | repo restore OWNER/NAME | repo check OWNER/NAME | repo size OWNER/NAME
  repo resync OWNER/NAME | repo resync --all | repo move-leader OWNER/NAME NODE
  repo archive OWNER/NAME | repo unarchive OWNER/NAME
  repo mirror OWNER/NAME        (push to its configured mirror now; leader only)
  release create OWNER/NAME --as USER FILE      (FILE: tag, title, notes; the tag must exist)
  release asset OWNER/NAME TAG --as USER [--mime TYPE] FILE
  announce TEXT | announce --clear      (notice shown on the front page)
  pop status | pop drain | pop undrain
  maintenance [--check]
  backup --out FILE.tar.gz | FILE.db
  restore --in FILE.tar.gz      (daemon must be stopped)
`)
}

func runAdmin(args []string) error {
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	cfgPath := fs.String("config", envOr("FORGE_CONFIG", ""), "configuration file")
	fs.Usage = adminUsage
	// --config may appear anywhere ("forge admin backup --config X --out Y"
	// is what units and docs naturally write); lift it out before parsing.
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" || args[i] == "-config":
			if i+1 < len(args) {
				_ = fs.Set("config", args[i+1])
				i++
			}
		case strings.HasPrefix(args[i], "--config="):
			_ = fs.Set("config", strings.TrimPrefix(args[i], "--config="))
		case strings.HasPrefix(args[i], "-config="):
			_ = fs.Set("config", strings.TrimPrefix(args[i], "-config="))
		default:
			rest = append(rest, args[i])
		}
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	rest = fs.Args()
	if len(rest) == 0 {
		adminUsage()
		return errors.New("command required")
	}
	if rest[0] == "init" {
		return adminInit(rest[1:])
	}
	if *cfgPath == "" {
		*cfgPath = "/etc/forge/forge.toml"
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	cfg.LogLevel = "warn"
	log := newLogger(cfg)
	ctx := context.Background()
	app, err := openForge(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer app.Store.Close()
	if err := app.EnsureDirs(); err != nil {
		return err
	}
	switch rest[0] {
	case "status":
		return adminStatus(ctx, app)
	case "user":
		return adminUser(ctx, app, rest[1:])
	case "key":
		return adminKey(ctx, app, rest[1:])
	case "cert":
		return adminCert(ctx, app, rest[1:])
	case "repo":
		return adminRepo(ctx, app, rest[1:])
	case "release":
		return adminRelease(ctx, app, rest[1:])
	case "maintenance":
		return adminMaintenance(ctx, app, rest[1:])
	case "backup":
		return adminBackup(ctx, app, rest[1:])
	case "restore":
		return adminRestore(ctx, app, rest[1:])
	case "pop":
		return adminPop(ctx, app, rest[1:])
	case "announce":
		if len(rest) == 2 && rest[1] == "--clear" {
			return app.Store.SetSetting(ctx, "announcement", "")
		}
		if len(rest) < 2 {
			return errors.New("announce TEXT | announce --clear")
		}
		return app.Store.SetSetting(ctx, "announcement", strings.Join(rest[1:], " "))
	default:
		adminUsage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func adminInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	data := fs.String("data", "/var/lib/forge", "data directory")
	host := fs.String("hostname", "", "public hostname (required)")
	node := fs.String("node", "local", "node name")
	title := fs.String("title", "forge", "forge title")
	out := fs.String("write-config", "", "write configuration to this file")
	geminiListen := fs.String("gemini-listen", ":1965", "comma-separated Gemini listen addresses")
	sshListen := fs.String("ssh-listen", ":22", "comma-separated SSH listen addresses")
	sshPort := fs.Int("ssh-port", 0, "public SSH port for clone URLs (default from listen address)")
	geminiPort := fs.Int("gemini-port", 0, "public Gemini port for URLs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		return errors.New("--hostname is required")
	}
	abs, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	cfg := config.Default(abs)
	cfg.Hostname, cfg.Node, cfg.Title = *host, *node, *title
	cfg.Gemini.Listen = strings.Split(*geminiListen, ",")
	cfg.SSH.Listen = strings.Split(*sshListen, ",")
	if *sshPort != 0 {
		cfg.SSH.Port = *sshPort
	} else if _, p, err := net.SplitHostPort(cfg.SSH.Listen[0]); err == nil {
		fmt.Sscanf(p, "%d", &cfg.SSH.Port)
	}
	if *geminiPort != 0 {
		cfg.Gemini.Port = *geminiPort
	} else if _, p, err := net.SplitHostPort(cfg.Gemini.Listen[0]); err == nil {
		fmt.Sscanf(p, "%d", &cfg.Gemini.Port)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return err
	}
	ctx := context.Background()
	log := newLogger(cfg)
	app, err := openForge(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer app.Store.Close()
	if err := app.EnsureDirs(); err != nil {
		return err
	}
	if *out != "" {
		if err := cfg.Write(*out); err != nil {
			return err
		}
		fmt.Println("wrote", *out)
	}
	fmt.Println("initialised", abs)
	return nil
}

func adminStatus(ctx context.Context, app *forge.Forge) error {
	ver, _ := app.Store.SchemaVersion(ctx)
	users, _ := app.Store.CountUsers(ctx)
	repos, _ := app.Store.AllRepos(ctx)
	free, _ := app.FreeDisk()
	var total int64
	for _, r := range repos {
		total += r.SizeBytes
	}
	fmt.Printf("node:        %s\nhostname:    %s\ndata:        %s\nschema:      v%d\nusers:       %d\nrepos:       %d (%d bytes)\ndisk free:   %d bytes\ngit:         %s\n",
		app.Config.Node, app.Config.Hostname, app.Config.DataDir, ver, users, len(repos), total, free, app.Git.Version(ctx))
	return nil
}

func adminUser(ctx context.Context, app *forge.Forge, args []string) error {
	if len(args) == 0 {
		return errors.New("user: subcommand required")
	}
	switch args[0] {
	case "list":
		users, err := app.Store.ListUsers(ctx, 10000, 0)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tADMIN\tDISABLED\tCREATED")
		for _, u := range users {
			fmt.Fprintf(tw, "%d\t%s\t%v\t%v\t%s\n", u.ID, u.Name, u.Admin, u.Disabled, u.CreatedAt.Format("2006-01-02"))
		}
		return tw.Flush()
	case "create":
		fs := flag.NewFlagSet("user create", flag.ContinueOnError)
		admin := fs.Bool("admin", false, "grant administrator")
		if err := parseMixed(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("user create NAME")
		}
		if err := forge.ValidUserName(fs.Arg(0)); err != nil {
			return err
		}
		u, err := app.Store.CreateUser(ctx, fs.Arg(0), *admin)
		if err != nil {
			return err
		}
		fmt.Printf("created user %s (id %d)\n", u.Name, u.ID)
		return nil
	case "disable", "enable":
		if len(args) != 2 {
			return errors.New("user disable|enable NAME")
		}
		u, err := app.Store.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		return app.Store.SetUserDisabled(ctx, u.ID, args[0] == "disable")
	case "admin":
		fs := flag.NewFlagSet("user admin", flag.ContinueOnError)
		revoke := fs.Bool("revoke", false, "remove administrator")
		if err := parseMixed(fs, args[1:]); err != nil {
			return err
		}
		u, err := app.Store.UserByName(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		return app.Store.SetUserAdmin(ctx, u.ID, !*revoke)
	}
	return fmt.Errorf("user: unknown subcommand %q", args[0])
}

func adminKey(ctx context.Context, app *forge.Forge, args []string) error {
	if len(args) == 0 {
		return errors.New("key: subcommand required")
	}
	switch args[0] {
	case "add":
		if len(args) != 3 {
			return errors.New("key add USER FILE")
		}
		u, err := app.Store.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		data, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		keys, err := app.AddSSHKeys(ctx, u, string(data))
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Println("added", k.Fingerprint)
		}
		return nil
	case "list":
		if len(args) != 2 {
			return errors.New("key list USER")
		}
		u, err := app.Store.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		keys, err := app.Store.ListSSHKeys(ctx, u.ID)
		if err != nil {
			return err
		}
		for _, k := range keys {
			state := "active"
			if !k.RevokedAt.IsZero() {
				state = "revoked"
			}
			fmt.Printf("%s %s %s %s\n", k.Fingerprint, k.KeyType, state, k.Label)
		}
		return nil
	case "revoke":
		if len(args) != 2 {
			return errors.New("key revoke FINGERPRINT")
		}
		k, err := app.Store.SSHKeyByFingerprint(ctx, args[1])
		if err != nil {
			return err
		}
		return app.Store.RevokeSSHKey(ctx, k.ID)
	}
	return fmt.Errorf("key: unknown subcommand %q", args[0])
}

func adminCert(ctx context.Context, app *forge.Forge, args []string) error {
	if len(args) == 0 {
		return errors.New("cert: subcommand required")
	}
	switch args[0] {
	case "list":
		if len(args) != 2 {
			return errors.New("cert list USER")
		}
		u, err := app.Store.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		certs, err := app.Store.ListCertificates(ctx, u.ID)
		if err != nil {
			return err
		}
		for _, c := range certs {
			state := "active"
			if !c.RevokedAt.IsZero() {
				state = "revoked"
			}
			fmt.Printf("%s %s %s expires=%s last=%s\n", c.SPKISHA256, state, c.Label, c.NotAfter.Format("2006-01-02"), c.LastUsedAt.Format(time.RFC3339))
		}
		return nil
	case "revoke":
		if len(args) != 2 {
			return errors.New("cert revoke SPKI")
		}
		c, err := app.Store.CertificateBySPKI(ctx, args[1])
		if err != nil {
			return err
		}
		return app.Store.RevokeCertificate(ctx, c.ID)
	case "enrol-code":
		if len(args) != 2 {
			return errors.New("cert enrol-code USER")
		}
		u, err := app.Store.UserByName(ctx, args[1])
		if err != nil {
			return err
		}
		code, err := forge.NewEnrolmentCode(ctx, app, u)
		if err != nil {
			return err
		}
		fmt.Printf("enrolment code for %s (valid %s): %s\n", u.Name, forge.EnrolmentTokenTTL, code)
		return nil
	}
	return fmt.Errorf("cert: unknown subcommand %q", args[0])
}

// parseMixed parses flags that may appear after positional arguments.
func parseMixed(fs *flag.FlagSet, args []string) error {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if !strings.Contains(a, "=") {
				name := strings.TrimLeft(a, "-")
				if f := fs.Lookup(name); f != nil {
					if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
						if i+1 < len(args) {
							i++
							flags = append(flags, args[i])
						}
					}
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	return fs.Parse(append(flags, pos...))
}

func splitRepo(s string) (string, string, error) {
	s = strings.TrimPrefix(s, "~")
	owner, name, ok := strings.Cut(s, "/")
	if !ok {
		return "", "", errors.New("expected OWNER/NAME")
	}
	return owner, strings.TrimSuffix(name, ".git"), nil
}

func adminRepo(ctx context.Context, app *forge.Forge, args []string) error {
	if len(args) == 0 {
		return errors.New("repo: subcommand required")
	}
	switch args[0] {
	case "list":
		repos, err := app.Store.AllRepos(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tREPO\tPRIVATE\tARCHIVED\tLEADER\tSIZE\tUPDATED")
		for _, r := range repos {
			fmt.Fprintf(tw, "%d\t%s/%s\t%v\t%v\t%s\t%d\t%s\n", r.ID, r.Owner, r.Name, r.Private, r.Archived, r.LeaderNode, r.SizeBytes, r.UpdatedAt.Format("2006-01-02"))
		}
		return tw.Flush()
	case "create":
		fs := flag.NewFlagSet("repo create", flag.ContinueOnError)
		private := fs.Bool("private", false, "private repository")
		desc := fs.String("description", "", "description")
		if err := parseMixed(fs, args[1:]); err != nil {
			return err
		}
		owner, name, err := splitRepo(fs.Arg(0))
		if err != nil {
			return err
		}
		u, err := app.Store.UserByName(ctx, owner)
		if err != nil {
			return err
		}
		r, err := app.CreateRepo(ctx, u, forge.CreateRepoOptions{Name: name, Private: *private, Description: *desc})
		if err != nil {
			return err
		}
		fmt.Printf("created %s/%s at %s\n", r.Owner, r.Name, app.RepoPath(r.Owner, r.Name))
		return nil
	case "mirror":
		if len(args) != 2 {
			return errors.New("repo mirror OWNER/NAME")
		}
		owner, name, err := splitRepo(args[1])
		if err != nil {
			return err
		}
		if _, err := app.Store.RepoByPath(ctx, owner, name); err != nil {
			return err
		}
		w := newMirrorWorker(app.Config, app, nil, app.Log)
		if !w.Enabled() {
			return fmt.Errorf("mirroring is disabled: %s", w.Reason())
		}
		start := time.Now()
		if err := w.Push(ctx, owner+"/"+name); err != nil {
			return err
		}
		fmt.Printf("mirrored %s/%s to %s in %s\n", owner, name, mirror.Redact(app.Config.MirrorTargets()[owner+"/"+name]), time.Since(start).Round(time.Millisecond))
		return nil
	case "archive", "unarchive":
		if len(args) != 2 {
			return fmt.Errorf("repo %s OWNER/NAME", args[0])
		}
		owner, name, err := splitRepo(args[1])
		if err != nil {
			return err
		}
		r, err := app.Store.RepoByPath(ctx, owner, name)
		if err != nil {
			return err
		}
		o, err := app.Store.UserByID(ctx, r.OwnerID)
		if err != nil {
			return err
		}
		arch := args[0] == "archive"
		return app.UpdateRepo(ctx, o, r, forge.UpdateRepoOptions{Archived: &arch})
	case "resync", "move-leader":
		if len(args) < 2 {
			return fmt.Errorf("repo %s OWNER/NAME [NODE]", args[0])
		}
		if args[0] == "resync" && args[1] == "--all" {
			node, err := newReplNode(app)
			if err != nil {
				return err
			}
			repos, err := app.Store.AllRepos(ctx)
			if err != nil {
				return err
			}
			failed := 0
			for _, r := range repos {
				if r.LeaderNode == app.Config.Node {
					continue
				}
				if err := node.Resync(ctx, r.ID); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "resync %s/%s: %v\n", r.Owner, r.Name, err)
				} else {
					fmt.Printf("resynced %s/%s\n", r.Owner, r.Name)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d repositories failed to resync", failed)
			}
			return nil
		}
		owner, name, err := splitRepo(args[1])
		if err != nil {
			return err
		}
		r, err := app.Store.RepoByPath(ctx, owner, name)
		if err != nil {
			return err
		}
		node, err := newReplNode(app)
		if err != nil {
			return err
		}
		if args[0] == "resync" {
			return node.Resync(ctx, r.ID)
		}
		if len(args) != 3 {
			return errors.New("repo move-leader OWNER/NAME NODE")
		}
		return node.MoveLeader(ctx, r.ID, args[2])
	case "delete", "restore", "check", "size":
		if len(args) != 2 {
			return fmt.Errorf("repo %s OWNER/NAME", args[0])
		}
		owner, name, err := splitRepo(args[1])
		if err != nil {
			return err
		}
		if args[0] == "restore" {
			repos, err := app.Store.ListDeletedRepos(ctx, time.Now().Add(time.Hour))
			if err != nil {
				return err
			}
			for _, r := range repos {
				if r.Owner == owner && r.Name == name {
					return app.Store.RestoreRepo(ctx, r.ID)
				}
			}
			return errors.New("no such deleted repository")
		}
		r, err := app.Store.RepoByPath(ctx, owner, name)
		if err != nil {
			return err
		}
		switch args[0] {
		case "delete":
			owner, err := app.Store.UserByID(ctx, r.OwnerID)
			if err != nil {
				return err
			}
			admin := *owner
			admin.Admin = true
			if err := app.DeleteRepo(ctx, &admin, r, r.Owner+"/"+r.Name); err != nil {
				return err
			}
			fmt.Printf("deleted %s/%s; restorable for %s with 'repo restore'\n", r.Owner, r.Name, forge.DeleteRetention)
		case "check":
			repo, err := app.Open(r)
			if err != nil {
				return err
			}
			if err := repo.Check(ctx); err != nil {
				return err
			}
			fmt.Println("ok")
		case "size":
			n, err := app.RefreshSize(ctx, r)
			if err != nil {
				return err
			}
			fmt.Println(n)
		}
		return nil
	}
	return fmt.Errorf("repo: unknown subcommand %q", args[0])
}

// adminRelease publishes releases from the command line (the release
// procedure, docs/release-procedure.md) with the same checks as a Titan
// upload: the acting user must have write access and the tag must exist.
func adminRelease(ctx context.Context, app *forge.Forge, args []string) error {
	usage := errors.New("release create OWNER/NAME --as USER FILE | release asset OWNER/NAME TAG --as USER [--mime TYPE] FILE")
	if len(args) == 0 {
		return usage
	}
	access := func(as, repo string) (*store.User, forge.Access, error) {
		if as == "" {
			return nil, forge.Access{}, errors.New("--as USER is required")
		}
		owner, name, err := splitRepo(repo)
		if err != nil {
			return nil, forge.Access{}, err
		}
		u, err := app.Store.UserByName(ctx, as)
		if err != nil {
			return nil, forge.Access{}, err
		}
		acc, err := app.LookupRepo(ctx, u, owner, name)
		if err != nil {
			return nil, forge.Access{}, err
		}
		return u, acc, nil
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("release create", flag.ContinueOnError)
		as := fs.String("as", "", "acting user")
		if err := parseMixed(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 2 {
			return usage
		}
		u, acc, err := access(*as, fs.Arg(0))
		if err != nil {
			return err
		}
		text, err := os.ReadFile(fs.Arg(1))
		if err != nil {
			return err
		}
		rel, err := app.CreateRelease(ctx, u, acc, string(text))
		if err != nil {
			return err
		}
		fmt.Printf("released %s/%s %s: %s\n", acc.Repo.Owner, acc.Repo.Name, rel.Tag, rel.Title)
		return nil
	case "asset":
		fs := flag.NewFlagSet("release asset", flag.ContinueOnError)
		as := fs.String("as", "", "acting user")
		mimeType := fs.String("mime", "", "MIME type (default: by extension, else application/octet-stream)")
		if err := parseMixed(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 3 {
			return usage
		}
		u, acc, err := access(*as, fs.Arg(0))
		if err != nil {
			return err
		}
		rel, err := app.LookupRelease(ctx, acc, fs.Arg(1))
		if err != nil {
			return err
		}
		f, err := os.Open(fs.Arg(2))
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		mt := *mimeType
		if mt == "" {
			mt = mime.TypeByExtension(filepath.Ext(fs.Arg(2)))
		}
		if mt == "" {
			mt = "application/octet-stream"
		}
		a, err := app.AddAsset(ctx, u, acc, rel, filepath.Base(fs.Arg(2)), mt, st.Size(), f)
		if err != nil {
			return err
		}
		fmt.Printf("attached %s (%d bytes, %s) to %s/%s %s\n", a.Name, a.Size, a.MIME, acc.Repo.Owner, acc.Repo.Name, rel.Tag)
		return nil
	}
	return usage
}

func adminMaintenance(ctx context.Context, app *forge.Forge, args []string) error {
	fs := flag.NewFlagSet("maintenance", flag.ContinueOnError)
	check := fs.Bool("check", false, "also run git fsck on every repository")
	if err := fs.Parse(args); err != nil {
		return err
	}
	stats, err := app.Maintain(ctx, *check)
	for k, v := range stats {
		fmt.Printf("%s: %d\n", k, v)
	}
	if err != nil {
		return err
	}
	if stats["corrupt"] > 0 {
		return fmt.Errorf("%d repositories failed fsck", stats["corrupt"])
	}
	return nil
}

func adminBackup(ctx context.Context, app *forge.Forge, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := fs.String("out", "", "output path: FILE.tar.gz (full) or FILE.db (database only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	if err := app.Backup(ctx, *out); err != nil {
		return err
	}
	fmt.Println("backup written to", *out)
	return nil
}

func adminRestore(ctx context.Context, app *forge.Forge, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	in := fs.String("in", "", "backup archive (FILE.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("--in is required")
	}
	if _, err := os.Stat(app.Config.HookSocket()); err == nil {
		return errors.New("hook socket exists: stop the daemon before restoring")
	}
	if err := app.Restore(ctx, *in); err != nil {
		return err
	}
	fmt.Println("restored from", *in)
	return nil
}

func newReplNode(app *forge.Forge) (*repl.Node, error) {
	c := app.Config.Cluster
	if !c.Enabled {
		return nil, errors.New("cluster mode is not enabled in the configuration")
	}
	return repl.New(repl.Options{
		Name: app.Config.Node, Listen: c.ControlListen, Peers: c.Peers, SecretFile: c.SecretFile,
		MetadataLeader: c.MetadataLeader, SyncInterval: c.SyncInterval.Duration, ReposDir: app.Config.ReposDir(), AssetsDir: app.Config.AssetsDir(),
		Version: version.Version, Store: app.Store, Git: app.Git, Log: app.Log,
	})
}

func adminPop(ctx context.Context, app *forge.Forge, args []string) error {
	if len(args) == 0 {
		return errors.New("pop: subcommand required (status)")
	}
	switch args[0] {
	case "drain":
		if err := os.WriteFile(app.Config.DrainFile(), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o640); err != nil {
			return err
		}
		fmt.Println("drain requested; the daemon prepends/withdraws within a few seconds and stays drained until 'pop undrain'")
		return nil
	case "undrain":
		if err := os.Remove(app.Config.DrainFile()); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("drain released; the daemon re-announces once checks pass")
		return nil
	case "status":
		fmt.Printf("node: %s\n", app.Config.Node)
		if _, err := os.Stat(app.Config.DrainFile()); err == nil {
			fmt.Println("drain: pinned by operator (pop undrain to release)")
		}
		if !app.Config.Cluster.Enabled {
			fmt.Println("cluster: disabled (single node)")
			return nil
		}
		st, err := repl.Status(ctx, app.Store)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "REPO\tLEADER\tNODE\tSTATUS\tLAST SYNC\tDETAIL")
		for _, s := range st {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Repo, s.LeaderNode, s.Node, s.Status, s.LastSyncedAt.Format(time.RFC3339), s.Detail)
		}
		return tw.Flush()
	}
	return fmt.Errorf("pop: unknown subcommand %q", args[0])
}

var _ = store.ErrNotFound
