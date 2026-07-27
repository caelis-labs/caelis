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
			DiffRemove:    profileColor(profile, "#d08770", "131", "1"),
			DiffLineNo:    profileColor(profile, "#7f8a9d", "244", "8"),
			Scrollbar:     profileColor(profile, "#81a1c1", "110", "7"),
		},
		themeSurfaces{
			App:              profileColor(profile, "#2e3440", "236", ""),
			Base:             profileColor(profile, "#3b4252", "237", ""),
			Raised:           profileColor(profile, "#434c5e", "239", ""),
			User:             profileColor(profile, "#343a47", "236", ""),
			Composer:         profileColor(profile, "#3b4252", "237", ""),
			Selection:        profileColor(profile, "#4c566a", "240", ""),
			SelectionText:    profileColor(profile, "#eceff4", "255", "7"),
			OnAccent:         profileColor(profile, "#2e3440", "236", "0"),
			DiffAdd:          profileColor(profile, "#314236", "23", ""),
			DiffAddStrong:    profileColor(profile, "#45604e", "59", ""),
			DiffRemove:       profileColor(profile, "#4a3037", "52", ""),
			DiffRemoveStrong: profileColor(profile, "#6a3f4a", "95", ""),
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
			TextSecondary: profileColor(profile, "#d7c2ff", "183", "7"),
			Muted:         profileColor(profile, "#a693d1", "140", "8"),
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
			DiffLineNo:    profileColor(profile, "#8a8fa8", "245", "8"),
			Scrollbar:     profileColor(profile, "#6272a4", "61", "7"),
		},
		themeSurfaces{
			App:              profileColor(profile, "#282a36", "236", ""),
			Base:             profileColor(profile, "#1f2130", "235", ""),
			Raised:           profileColor(profile, "#343746", "237", ""),
			User:             profileColor(profile, "#302b3b", "236", ""),
			Composer:         profileColor(profile, "#1f2130", "235", ""),
			Selection:        profileColor(profile, "#44475a", "239", ""),
			SelectionText:    profileColor(profile, "#f8f8f2", "255", "7"),
			OnAccent:         profileColor(profile, "#282a36", "236", "0"),
			DiffAdd:          profileColor(profile, "#21392a", "22", ""),
			DiffAddStrong:    profileColor(profile, "#2f5f43", "29", ""),
			DiffRemove:       profileColor(profile, "#4a232d", "52", ""),
			DiffRemoveStrong: profileColor(profile, "#7d3243", "89", ""),
		},
	)
}
