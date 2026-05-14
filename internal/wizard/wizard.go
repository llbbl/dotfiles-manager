package wizard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/llbbl/dotfiles-manager/internal/config"
	"github.com/llbbl/dotfiles-manager/internal/fsx"
)

// Options bundles the non-interactive overrides + the I/O streams the
// wizard speaks through. Zero values are valid: the wizard prompts for
// anything not overridden.
type Options struct {
	// ConfigPath is the file the wizard intends to write. Empty means
	// "use config.DefaultPath()".
	ConfigPath string

	// Yes accepts every default. Skips Turso, skips repo creation,
	// skips the track-nudge.
	Yes bool

	// Force overwrites an existing config without entering edit-mode
	// pre-fills.
	Force bool

	// Print prevents any file write; the rendered TOML is emitted to
	// Out and the rest of the side effects are skipped.
	Print bool

	// State selects the state-store chapter branch when non-empty.
	// "local" or "turso". Empty = ask.
	State string

	// Repo/Turso non-interactive overrides. When any of these is set
	// the corresponding chapter does not prompt.
	RemoteURL      string
	CreateRemote   bool
	Turso          bool
	TursoDBName    string
	TursoURL       string
	TursoAuthToken string

	// AI-chapter overrides.
	AIBin   string
	AIModel string

	// I/O streams. In is the prompt source (typically os.Stdin). Out
	// receives prompts and summaries; the wizard never writes anything
	// to stderr — the caller decides.
	In  io.Reader
	Out io.Writer
}

// Plan is what Run returns: the chosen config plus any side-effect
// intents the cobra layer must execute (provision a Turso DB, init the
// backup repo, run `dfm track`, etc.). The wizard itself only writes
// the config file (atomically, mode 0600) and never shells out.
type Plan struct {
	// Config is the fully-populated *config.Config the wizard wrote (or
	// would write, in --print mode).
	Config *config.Config

	// ConfigPath is the resolved destination of the TOML file.
	ConfigPath string

	// ConfigWritten is true when the wizard actually persisted Config
	// to ConfigPath. False in --print mode.
	ConfigWritten bool

	// ProvisionTurso is true when the user picked the Turso branch and
	// did NOT bake values in from env / flags — i.e. the cobra layer
	// should run `runTursoInit` to create the DB.
	ProvisionTurso bool
	TursoDBName    string

	// RepoFlow is true when chapter 4 produced a non-empty remote and
	// the cobra layer should run the existing repo-init flow.
	RepoFlow     bool
	CreateRemote bool

	// TrackPath is non-empty when the user accepted the track-nudge.
	TrackPath string

	// Tip is the one-line "try this next" message printed in chapter 6.
	Tip string
}

// Run drives all six chapters and returns the resulting Plan. When the
// caller asked for --print the config is rendered to opts.Out and not
// written; otherwise it is persisted atomically with mode 0600.
//
// existing is the *config.Config currently on disk at opts.ConfigPath,
// or nil when no config exists yet. When existing != nil AND
// !opts.Force, the wizard runs in "edit mode": each chapter pre-fills
// the existing value as the Enter default.
func Run(opts Options, existing *config.Config) (*Plan, error) {
	if opts.In == nil {
		opts.In = strings.NewReader("")
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	in := bufio.NewReader(opts.In)

	// Edit mode is active iff the config exists AND --force is NOT set.
	editMode := existing != nil && !opts.Force

	plan := &Plan{}

	// ---- Chapter 1: config path -----------------------------------
	cfgPath, err := chapterConfigPath(in, opts.Out, opts)
	if err != nil {
		return nil, err
	}
	plan.ConfigPath = cfgPath

	// Refuse to clobber an existing file when neither --force nor edit
	// mode is active. (Edit mode is the only way the wizard touches an
	// existing file without --force.)
	if !opts.Force && !editMode && fileExists(cfgPath) {
		return nil, fmt.Errorf("config already exists at %s (pass --force to overwrite or re-run without to edit)", cfgPath)
	}

	// Start from existing config in edit mode; otherwise Defaults().
	var cfg *config.Config
	if editMode {
		clone := *existing
		cfg = &clone
	} else {
		cfg = config.Defaults()
	}

	// ---- Chapter 2: AI --------------------------------------------
	if err := chapterAI(in, opts.Out, opts, cfg, editMode); err != nil {
		return nil, err
	}

	// ---- Chapter 3: state store -----------------------------------
	provisionTurso, dbName, err := chapterState(in, opts.Out, opts, cfg, editMode)
	if err != nil {
		return nil, err
	}
	plan.ProvisionTurso = provisionTurso
	plan.TursoDBName = dbName

	// ---- Chapter 4: backup repo -----------------------------------
	repoFlow, createRemote, err := chapterRepo(in, opts.Out, opts, cfg, editMode)
	if err != nil {
		return nil, err
	}
	plan.RepoFlow = repoFlow
	plan.CreateRemote = createRemote

	// ---- Chapter 5: track-nudge -----------------------------------
	trackPath, err := chapterTrack(in, opts.Out, opts)
	if err != nil {
		return nil, err
	}
	plan.TrackPath = trackPath

	plan.Config = cfg

	// Persist the config (or print it).
	data, err := cfg.EncodeTOML()
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	if opts.Print {
		if _, err := opts.Out.Write(data); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(cfgPath), err)
		}
		if err := fsx.AtomicWrite(cfgPath, data, 0o600); err != nil {
			return nil, err
		}
		plan.ConfigWritten = true
	}

	// ---- Chapter 6: summary ---------------------------------------
	plan.Tip = chapterSummary(opts.Out, plan, opts)

	return plan, nil
}

// LoadExisting is a small convenience for callers: returns the on-disk
// config if a TOML file lives at path, or (nil, nil) when it doesn't.
func LoadExisting(path string) (*config.Config, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return config.Load(path)
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// lookupClaude resolves "claude" on $PATH for the binary default. We
// keep it as a package var so tests can stub it without exec-ing.
var lookupClaude = func() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return "claude"
}
