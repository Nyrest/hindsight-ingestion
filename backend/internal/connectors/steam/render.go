package steam

import (
	"fmt"
	"strings"
	"time"
)

func md(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer("\\", "\\\\", "|", "\\|", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "<", "&lt;", ">", "&gt;").Replace(s)
}
func date(unix int64) string {
	if unix <= 0 {
		return "Not reported"
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04 UTC")
}
func minutes(n int64) string { return fmt.Sprintf("%.2f hours (%d minutes)", float64(n)/60, n) }
func optionalMinutes(n *int64) string {
	if n == nil {
		return "Not reported"
	}
	return minutes(*n)
}
func gameName(g game) string {
	if g.Name != "" {
		return md(g.Name)
	}
	return "Steam App " + appString(g.AppID)
}

func renderProfile(data profileData) string {
	p := data.Player
	friends, private, games, recent := data.Friends, data.FriendsPrivate, data.Games, data.Recent
	var b strings.Builder
	fmt.Fprintf(&b, "# Steam Profile: %s\n\n## Profile\n\n- Steam ID: %s\n- Display Name: %s\n- Profile URL: %s\n- Visibility: %d\n- Online State: %d\n", md(p.Username), p.SteamID, md(p.Username), md(p.ProfileURL), p.Visibility, p.State)
	for _, field := range []struct{ label, value string }{{"Avatar URL", p.Avatar}, {"Real Name", p.RealName}, {"Country", p.Country}, {"Current Game", p.CurrentGame}, {"Current App ID", p.GameID}} {
		if field.value != "" {
			fmt.Fprintf(&b, "- %s: %s\n", field.label, md(field.value))
		}
	}
	if p.Created > 0 {
		fmt.Fprintf(&b, "- Account Created: %s\n", date(p.Created))
	}
	if p.LastLogoff > 0 {
		fmt.Fprintf(&b, "- Last Online: %s\n", date(p.LastLogoff))
	}
	b.WriteString("\n## Friends\n\n")
	if private {
		b.WriteString("- Status: Unavailable\n- Reason: Steam friend list is private.\n")
	} else {
		fmt.Fprintf(&b, "- Total Visible Friends: %d\n\n| Steam ID | Relationship | Friends Since |\n| --- | --- | --- |\n", len(friends))
		for _, f := range friends {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", md(f.SteamID), md(f.Relationship), date(f.Since))
		}
	}
	fmt.Fprintf(&b, "\n## Owned Games\n\n- Games Reported: %d\n\n| Game | App ID | Total Playtime | Last 2 Weeks |\n| --- | --- | --- | --- |\n", len(games))
	for _, g := range games {
		fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", gameName(g), g.AppID, minutes(g.Playtime), optionalMinutes(g.Recent))
	}
	fmt.Fprintf(&b, "\n## Recently Played (Last 2 Weeks)\n\n- Games Reported: %d\n", len(recent))
	for _, g := range recent {
		fmt.Fprintf(&b, "- %s (App ID: %d): %s\n", gameName(g), g.AppID, optionalMinutes(g.Recent))
	}
	return b.String()
}

func renderGame(p player, g game, a achievements, includeAchievements bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n## Player\n\n- Steam ID: %s\n- Username: %s\n- Profile: %s\n\n## Game Information\n\n- App ID: %d\n- Game: %s\n- Store URL: https://store.steampowered.com/app/%d/\n", gameName(g), p.SteamID, md(p.Username), md(p.ProfileURL), g.AppID, gameName(g), g.AppID)
	if g.Icon != "" {
		fmt.Fprintf(&b, "- Icon URL: https://media.steampowered.com/steamcommunity/public/images/apps/%d/%s.jpg\n", g.AppID, md(g.Icon))
	}
	fmt.Fprintf(&b, "\n## Playtime\n\n- Total: %s\n- Last 2 Weeks: %s\n- Last Played: %s\n", minutes(g.Playtime), optionalMinutes(g.Recent), date(g.LastPlayed))
	for _, field := range []struct {
		label string
		value *int64
	}{{"Windows", g.Windows}, {"macOS", g.Mac}, {"Linux", g.Linux}, {"Steam Deck", g.Deck}, {"Offline", g.Disconnected}} {
		if field.value != nil {
			fmt.Fprintf(&b, "- %s: %s\n", field.label, minutes(*field.value))
		}
	}
	fmt.Fprintf(&b, "\n## Achievement Progress\n\n- Status: %s\n", a.Status)
	if a.Reason != "" {
		fmt.Fprintf(&b, "- Reason: %s\n", md(a.Reason))
	}
	unlocked, locked, unknown := 0, 0, 0
	for _, entry := range a.Items {
		switch entry.State {
		case "Unlocked":
			unlocked++
		case "Locked":
			locked++
		default:
			unknown++
		}
	}
	if a.Status == "Unavailable" && len(a.Items) == 0 {
		b.WriteString("- Total: Unavailable\n")
	} else {
		fmt.Fprintf(&b, "- Total: %d\n", len(a.Items))
	}
	if unknown == 0 && a.Status != "Unavailable" && a.Status != "Partial" {
		fmt.Fprintf(&b, "- Unlocked: %d\n- Locked: %d\n", unlocked, locked)
		if len(a.Items) > 0 {
			fmt.Fprintf(&b, "- Completion: %.2f%%\n", float64(unlocked)*100/float64(len(a.Items)))
		}
	} else {
		fmt.Fprintf(&b, "- Known Unlocked: %d\n- Known Locked: %d\n- Unknown: %d\n- Completion: Unavailable\n", unlocked, locked, unknown)
	}
	if !includeAchievements {
		return b.String()
	}
	for _, state := range []string{"Unlocked", "Locked"} {
		entries := 0
		for _, entry := range a.Items {
			if entry.State == state {
				entries++
			}
		}
		if entries == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s Achievements\n", state)
		for _, entry := range a.Items {
			if entry.State != state {
				continue
			}
			fmt.Fprintf(&b, "\n### %s\n\n- API Name: %s\n- Status: %s\n", md(entry.Name), md(entry.APIName), state)
			if entry.Description != "" {
				fmt.Fprintf(&b, "- Description: %s\n", md(entry.Description))
			}
			if entry.Hidden {
				b.WriteString("- Hidden: Yes\n")
			}
			if state == "Unlocked" && entry.UnlockedAt > 0 {
				fmt.Fprintf(&b, "- Unlocked At: %s\n", date(entry.UnlockedAt))
			}
			if entry.Icon != "" {
				fmt.Fprintf(&b, "- Icon: %s\n", md(entry.Icon))
			}
			if entry.LockedIcon != "" {
				fmt.Fprintf(&b, "- Locked Icon: %s\n", md(entry.LockedIcon))
			}
		}
	}
	return b.String()
}
