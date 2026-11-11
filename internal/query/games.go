package query

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// Game is a running game and the processes that belong to it.
type Game struct {
	Launcher string // steam, heroic, gamescope, or env and process for configured markers
	ID       string // Steam's app id, Heroic's app name, a marker's value, or a pid
	Name     string // what to call it
	Total    Total
}

// findGames marks the processes that belong to a game (docs/design.md,
// "The load verdict"): a process that carries a marker, and everything
// below it. Agents' own processes never count: a test that an agent runs
// under a headless gamescope isn't somebody playing.
func (v *View) findGames(o Options) {
	steamName := o.SteamName
	index := map[string]int{}
	game := func(launcher, id, name string) int {
		key := launcher + ":" + id
		i, ok := index[key]
		if !ok {
			i = len(v.Games)
			index[key] = i
			v.Games = append(v.Games, Game{Launcher: launcher, ID: id, Name: name})
		}
		return i
	}
	own := func(p *Proc) int {
		if p.Scope >= 0 || p.Kernel {
			return -1
		}
		if id := steamApp(p); id != "" {
			name := ""
			if steamName != nil {
				name = steamName(id)
			}
			if name == "" {
				name = "Steam app " + id
			}
			return game("steam", id, name)
		}
		if app := p.Env["HEROIC_APP_NAME"]; app != "" {
			name := app
			if src := p.Env["HEROIC_APP_SOURCE"]; src != "" {
				name += " (" + src + ")"
			}
			return game("heroic", app, name)
		}
		if p.Comm == "gamescope" || Name(p) == "gamescope" {
			name := "gamescope"
			for i, a := range p.Args {
				if a == "--" && i+1 < len(p.Args) {
					name = path.Base(p.Args[i+1])
				}
			}
			return game("gamescope", strconv.Itoa(p.PID), name)
		}
		for _, k := range o.GameEnv {
			if val := p.Env[k]; val != "" {
				return game("env", k+"="+val, val)
			}
		}
		if name := Name(p); slices.Contains(o.GameProcess, name) || slices.Contains(o.GameProcess, p.Comm) {
			return game("process", strconv.Itoa(p.PID), name)
		}
		return -1
	}
	// Parents have lower indexes than their children only by accident, so
	// resolve each process by walking up, and remember the answers.
	done := make([]bool, len(v.Procs))
	var resolve func(i int, depth int) int
	resolve = func(i, depth int) int {
		p := &v.Procs[i]
		if done[i] {
			return p.Game
		}
		done[i] = true
		p.Game = own(p)
		if p.Game < 0 && p.Scope < 0 && depth < 256 {
			if pi, ok := v.byPID[p.PPID]; ok && p.PPID != p.PID {
				p.Game = resolve(pi, depth+1)
			}
		}
		return p.Game
	}
	for i := range v.Procs {
		if g := resolve(i, 0); g >= 0 {
			v.Games[g].Total.add(&v.Procs[i])
		}
	}
}

// steamApp returns the Steam app a process was started for: from Steam's
// "reaper SteamLaunch AppId=<id>" wrapper, or from the variables Steam
// sets for the game. Steam's own client has neither.
func steamApp(p *Proc) string {
	if Name(p) == "reaper" || p.Comm == "reaper" {
		launch := false
		for _, a := range p.Args {
			if a == "SteamLaunch" {
				launch = true
			}
			if id, ok := strings.CutPrefix(a, "AppId="); ok && launch && id != "" && id != "0" {
				return id
			}
		}
	}
	for _, k := range []string{"SteamAppId", "SteamGameId"} {
		if id := p.Env[k]; id != "" && id != "0" {
			return id
		}
	}
	return ""
}
