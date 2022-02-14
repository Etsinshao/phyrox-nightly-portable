//go:generate go install -v github.com/kevinburke/go-bindata/go-bindata
//go:generate go-bindata -prefix res/ -pkg assets -o assets/assets.go res/FirefoxNightly.lnk
//go:generate go install -v github.com/josephspurrier/goversioninfo/cmd/goversioninfo
//go:generate goversioninfo -icon=res/papp.ico -manifest=res/papp.manifest
package main

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/Jeffail/gabs"
	"github.com/pkg/errors"
	"github.com/portapps/phyrox-developer-portable/assets"
	"github.com/portapps/portapps/v3"
	"github.com/portapps/portapps/v3/pkg/log"
	"github.com/portapps/portapps/v3/pkg/mutex"
	"github.com/portapps/portapps/v3/pkg/shortcut"
	"github.com/portapps/portapps/v3/pkg/utl"
	"github.com/portapps/portapps/v3/pkg/win"
)

type config struct {
	Profile           string `yaml:"profile" mapstructure:"profile"`
	MultipleInstances bool   `yaml:"multiple_instances" mapstructure:"multiple_instances"`
	Cleanup           bool   `yaml:"cleanup" mapstructure:"cleanup"`
}

var (
	app *portapps.App
	cfg *config
)

func init() {
	var err error

	// Default config
	cfg = &config{
		Profile:           "default",
		MultipleInstances: false,
		Cleanup:           false,
	}

	// Init app
	if app, err = portapps.NewWithCfg("phyrox-nightly-portable", "Phyrox Nightly", cfg); err != nil {
		log.Fatal().Err(err).Msg("Cannot initialize application. See log file for more info.")
	}
}

func main() {
	utl.CreateFolder(app.DataPath)
	profileFolder := utl.CreateFolder(app.DataPath, "profile", cfg.Profile)

	app.Process = utl.PathJoin(app.AppPath, "firefox.exe")
	app.Args = []string{
		"--profile",
		profileFolder,
	}

	// Set env vars
	crashreporterFolder := utl.CreateFolder(app.DataPath, "crashreporter")
	pluginsFolder := utl.CreateFolder(app.DataPath, "plugins")
	os.Setenv("MOZ_CRASHREPORTER", "0")
	os.Setenv("MOZ_CRASHREPORTER_DATA_DIRECTORY", crashreporterFolder)
	os.Setenv("MOZ_CRASHREPORTER_DISABLE", "1")
	os.Setenv("MOZ_CRASHREPORTER_NO_REPORT", "1")
	os.Setenv("MOZ_DATA_REPORTING", "0")
	os.Setenv("MOZ_MAINTENANCE_SERVICE", "0")
	os.Setenv("MOZ_PLUGIN_PATH", pluginsFolder)
	os.Setenv("MOZ_UPDATER", "0")

	// Create and check mutex
	mu, err := mutex.Create(app.ID)
	defer mutex.Release(mu)
	if err != nil {
		if !cfg.MultipleInstances {
			log.Error().Msg("You have to enable multiple instances in your configuration if you want to launch another instance")
			if _, err = win.MsgBox(
				fmt.Sprintf("%s portable", app.Name),
				"Other instance detected. You have to enable multiple instances in your configuration if you want to launch another instance.",
				win.MsgBoxBtnOk|win.MsgBoxIconError); err != nil {
				log.Error().Err(err).Msg("Cannot create dialog box")
			}
			return
		} else {
			log.Warn().Msg("Another instance is already running")
		}
	}

	// Cleanup on exit
	if cfg.Cleanup {
		defer func() {
			utl.Cleanup([]string{
				path.Join(os.Getenv("APPDATA"), "Mozilla", "Firefox"),
				path.Join(os.Getenv("LOCALAPPDATA"), "Mozilla", "Firefox"),
				path.Join(os.Getenv("USERPROFILE"), "AppData", "LocalLow", "Mozilla"),
			})
		}()
	}

	// Multiple instances
	if cfg.MultipleInstances {
		log.Info().Msg("Multiple instances enabled")
		app.Args = append(app.Args, "--no-remote")
	}

	// Policies
	if err := createPolicies(); err != nil {
		log.Fatal().Err(err).Msg("Cannot create policies")
	}

	// Fix extensions path
	if err := updateAddonStartup(profileFolder); err != nil {
		log.Error().Err(err).Msg("Cannot fix extensions path")
	}

	// Copy default shortcut
	shortcutPath := path.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Phyrox Nightly Portable.lnk")
	defaultShortcut, err := assets.Asset("FirefoxNightly.lnk")
	if err != nil {
		log.Error().Err(err).Msg("Cannot load asset FirefoxNightly.lnk")
	}
	err = os.WriteFile(shortcutPath, defaultShortcut, 0644)
	if err != nil {
		log.Error().Err(err).Msg("Cannot write default shortcut")
	}

	// Update default shortcut
	err = shortcut.Create(shortcut.Shortcut{
		ShortcutPath:     shortcutPath,
		TargetPath:       app.Process,
		Arguments:        shortcut.Property{Clear: true},
		Description:      shortcut.Property{Value: "Phyrox Nightly Portable by Portapps"},
		IconLocation:     shortcut.Property{Value: app.Process},
		WorkingDirectory: shortcut.Property{Value: app.AppPath},
	})
	if err != nil {
		log.Error().Err(err).Msg("Cannot create shortcut")
	}
	defer func() {
		if err := os.Remove(shortcutPath); err != nil {
			log.Error().Err(err).Msg("Cannot remove shortcut")
		}
	}()

	defer app.Close()
	app.Launch(os.Args[1:])
}

func createPolicies() error {
	appFile := utl.PathJoin(utl.CreateFolder(app.AppPath, "distribution"), "policies.json")
	dataFile := utl.PathJoin(app.DataPath, "policies.json")
	defaultPolicies := struct {
		Policies map[string]interface{} `json:"policies"`
	}{
		Policies: map[string]interface{}{
			"DisableAppUpdate":        true,
			"DontCheckDefaultBrowser": true,
		},
	}

	jsonPolicies, err := gabs.Consume(defaultPolicies)
	if err != nil {
		return errors.Wrap(err, "Cannot consume default policies")
	}
	log.Debug().Msgf("Default policies: %s", jsonPolicies.String())

	if utl.Exists(dataFile) {
		rawCustomPolicies, err := os.ReadFile(dataFile)
		if err != nil {
			return errors.Wrap(err, "Cannot read custom policies")
		}

		jsonPolicies, err = gabs.ParseJSON(rawCustomPolicies)
		if err != nil {
			return errors.Wrap(err, "Cannot consume custom policies")
		}
		log.Debug().Msgf("Custom policies: %s", jsonPolicies.String())

		jsonPolicies.Set(true, "policies", "DisableAppUpdate")
		jsonPolicies.Set(true, "policies", "DontCheckDefaultBrowser")
	}

	log.Debug().Msgf("Applied policies: %s", jsonPolicies.String())
	err = os.WriteFile(appFile, []byte(jsonPolicies.StringIndent("", "  ")), 0644)
	if err != nil {
		return errors.Wrap(err, "Cannot write policies")
	}

	return nil
}

func updateAddonStartup(profileFolder string) error {
	lz4File := path.Join(profileFolder, "addonStartup.json.lz4")
	if !utl.Exists(lz4File) || app.Prev.RootPath == "" {
		return nil
	}

	lz4Raw, err := mozLz4Decompress(lz4File)
	if err != nil {
		return err
	}

	prevPathLin := strings.Replace(utl.FormatUnixPath(app.Prev.RootPath), ` `, `%20`, -1)
	currPathLin := strings.Replace(utl.FormatUnixPath(app.RootPath), ` `, `%20`, -1)
	lz4Str := strings.Replace(string(lz4Raw), prevPathLin, currPathLin, -1)

	prevPathWin := strings.Replace(strings.Replace(utl.FormatWindowsPath(app.Prev.RootPath), `\`, `\\`, -1), ` `, `%20`, -1)
	currPathWin := strings.Replace(strings.Replace(utl.FormatWindowsPath(app.RootPath), `\`, `\\`, -1), ` `, `%20`, -1)
	lz4Str = strings.Replace(lz4Str, prevPathWin, currPathWin, -1)

	lz4Enc, err := mozLz4Compress([]byte(lz4Str))
	if err != nil {
		return err
	}

	return os.WriteFile(lz4File, lz4Enc, 0644)
}