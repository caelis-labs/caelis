package tuikit

import "github.com/charmbracelet/colorprofile"

func nordTheme(profile colorprofile.Profile) Theme {
	return themeFrom(
		themePalette{
			Name:          "nord",
			IsDark:        true,
			TextPrimary:   profileColor(profile, "#eceff4", "255", "7"),
			TextSecondary: profileColor(profile, "#d8dee9", "252", "7"),
			Muted:         profileColor(profile, "#a7b0c0", "248", "8"),
			Info:          profileColor(profile, "#d8dee9", "252", "6"),
			Success:       profileColor(profile, "#a3be8c", "108", "2"),
			Warning:       profileColor(profile, "#ebcb8b", "223", "3"),
			Danger:        profileColor(profile, "#bf616a", "131", "1"),
			Accent:        profileColor(profile, "#88c0d0", "110", "6"),
			Focus:         profileColor(profile, "#81a1c1", "110", "4"),
			UserAccent:    profileColor(profile, "#d08770", "173", "3"),
			Tool:          profileColor(profile, "#88c0d0", "110", "6"),
			Border:        profileColor(profile, "#4c566a", "240", "8"),
			BorderStrong:  profileColor(profile, "#81a1c1", "110", "4"),
			DiffRemove:    profileColor(profile, "#bf616a", "131", "1"),
			DiffLineNo:    profileColor(profile, "#d8dee9", "253", "7"),
			Scrollbar:     profileColor(profile, "#81a1c1", "110", "7"),

			AgentMessageSent:     profileColor(profile, "#81a1c1", "110", "4"),
			AgentMessageReceived: profileColor(profile, "#8fbcbb", "109", "6"),
		},
		themeSurfaces{
			App:              profileColor(profile, "#2e3440", "236", ""),
			Base:             profileColor(profile, "#3b4252", "237", ""),
			Raised:           profileColor(profile, "#434c5e", "239", ""),
			User:             profileColor(profile, "#434c5e", "239", ""),
			Composer:         profileColor(profile, "#3b4252", "237", ""),
			Selection:        profileColor(profile, "#4c566a", "240", ""),
			SelectionText:    profileColor(profile, "#eceff4", "255", "7"),
			OnAccent:         profileColor(profile, "#2e3440", "236", "0"),
			DiffAdd:          paletteTint(profile, "#2e3440", "#a3be8c", .15),
			DiffAddStrong:    paletteTint(profile, "#2e3440", "#a3be8c", .25),
			DiffRemove:       paletteTint(profile, "#2e3440", "#bf616a", .15),
			DiffRemoveStrong: paletteTint(profile, "#2e3440", "#bf616a", .25),
		},
	)
}

func solarizedTheme(profile colorprofile.Profile) Theme {
	return themeFrom(
		themePalette{
			Name:          "solarized",
			IsDark:        true,
			TextPrimary:   profileColor(profile, "#eee8d5", "254", "7"),
			TextSecondary: profileColor(profile, "#b7c0bc", "250", "7"),
			Muted:         profileColor(profile, "#93a1a1", "245", "8"),
			Info:          profileColor(profile, "#93a1a1", "245", "6"),
			Success:       profileColor(profile, "#859900", "100", "2"),
			Warning:       profileColor(profile, "#b58900", "136", "3"),
			Danger:        profileColor(profile, "#dc322f", "160", "1"),
			Accent:        profileColor(profile, "#2aa198", "36", "6"),
			Focus:         profileColor(profile, "#268bd2", "32", "4"),
			UserAccent:    profileColor(profile, "#eee8d5", "254", "7"),
			Tool:          profileColor(profile, "#2aa198", "36", "6"),
			Border:        profileColor(profile, "#586e75", "242", "8"),
			BorderStrong:  profileColor(profile, "#657b83", "243", "8"),
			DiffLineNo:    profileColor(profile, "#839496", "244", "8"),
			Scrollbar:     profileColor(profile, "#586e75", "242", "7"),

			AgentMessageSent:     profileColor(profile, "#268bd2", "33", "4"),
			AgentMessageReceived: profileColor(profile, "#2aa198", "36", "6"),
		},
		themeSurfaces{
			App:              profileColor(profile, "#002b36", "235", ""),
			Base:             profileColor(profile, "#073642", "236", ""),
			Raised:           profileColor(profile, "#0d3e49", "237", ""),
			User:             profileColor(profile, "#123944", "237", ""),
			Composer:         profileColor(profile, "#073642", "236", ""),
			Selection:        profileColor(profile, "#134956", "238", ""),
			SelectionText:    profileColor(profile, "#fdf6e3", "230", "7"),
			OnAccent:         profileColor(profile, "#001014", "233", "0"),
			DiffAdd:          profileColor(profile, "#173d1c", "22", ""),
			DiffAddStrong:    profileColor(profile, "#2f5f2f", "29", ""),
			DiffRemove:       profileColor(profile, "#4a1f1c", "52", ""),
			DiffRemoveStrong: profileColor(profile, "#7a2d24", "88", ""),
		},
	)
}

func draculaTheme(profile colorprofile.Profile) Theme {
	return themeFrom(
		themePalette{
			Name:          "dracula",
			IsDark:        true,
			TextPrimary:   profileColor(profile, "#f8f8f2", "255", "7"),
			TextSecondary: profileColor(profile, "#f8f8f2", "255", "7"),
			Muted:         profileColor(profile, "#bd93f9", "141", "5"),
			Info:          profileColor(profile, "#8be9fd", "123", "6"),
			Success:       profileColor(profile, "#50fa7b", "84", "2"),
			Warning:       profileColor(profile, "#ffb86c", "215", "3"),
			Danger:        profileColor(profile, "#ff5555", "203", "1"),
			Accent:        profileColor(profile, "#ff79c6", "212", "5"),
			Focus:         profileColor(profile, "#8be9fd", "123", "6"),
			UserAccent:    profileColor(profile, "#ff79c6", "212", "5"),
			Tool:          profileColor(profile, "#8be9fd", "123", "6"),
			Border:        profileColor(profile, "#6272a4", "61", "8"),
			BorderStrong:  profileColor(profile, "#8be9fd", "123", "6"),
			DiffLineNo:    profileColor(profile, "#bd93f9", "141", "5"),
			Scrollbar:     profileColor(profile, "#6272a4", "61", "7"),

			AgentMessageSent:     profileColor(profile, "#bd93f9", "141", "5"),
			AgentMessageReceived: profileColor(profile, "#8be9fd", "117", "6"),
		},
		themeSurfaces{
			App:              profileColor(profile, "#282a36", "236", ""),
			Base:             profileColor(profile, "#282a36", "236", ""),
			Raised:           profileColor(profile, "#44475a", "239", ""),
			User:             profileColor(profile, "#44475a", "239", ""),
			Composer:         profileColor(profile, "#282a36", "236", ""),
			Selection:        profileColor(profile, "#44475a", "239", ""),
			SelectionText:    profileColor(profile, "#f8f8f2", "255", "7"),
			OnAccent:         profileColor(profile, "#282a36", "236", "0"),
			DiffAdd:          paletteTint(profile, "#282a36", "#50fa7b", .15),
			DiffAddStrong:    paletteTint(profile, "#282a36", "#50fa7b", .25),
			DiffRemove:       paletteTint(profile, "#282a36", "#ff5555", .15),
			DiffRemoveStrong: paletteTint(profile, "#282a36", "#ff5555", .25),
		},
	)
}
