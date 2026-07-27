package tuikit

import (
	"image/color"

	"github.com/charmbracelet/colorprofile"
)

func catppuccinAdaptiveThemeVariant(profile colorprofile.Profile, dark bool, background color.Color) Theme {
	if dark {
		return catppuccinMochaTheme(profile, background)
	}
	return catppuccinLatteTheme(profile, background)
}

func catppuccinMochaTheme(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, true, 0.08, 0, "#11111b", "", "233", "")
	surface2 := adaptiveBackgroundColor(profile, background, true, 0.12, 0, "#181825", "", "234", "")
	userSurface := adaptiveSurfaceTintColor(profile, background, true, [3]uint8{242, 205, 205}, 0.08, 0.04, "#1d191e", "", "234", "")

	return themeFrom(
		themePalette{
			Name:          "catppuccin-mocha",
			IsDark:        true,
			TextPrimary:   profileColor(profile, "#cdd6f4", "253", "7"),
			TextSecondary: profileColor(profile, "#a6adc8", "248", "7"),
			Muted:         profileColor(profile, "#7f849c", "243", "8"),
			Info:          profileColor(profile, "#89dceb", "117", "6"),
			Success:       profileColor(profile, "#a6e3a1", "114", "2"),
			Warning:       profileColor(profile, "#f9e2af", "221", "3"),
			Danger:        profileColor(profile, "#f38ba8", "204", "1"),
			Accent:        profileColor(profile, "#cba6f7", "183", "5"),
			Focus:         profileColor(profile, "#b4befe", "147", "6"),
			UserAccent:    profileColor(profile, "#f2cdcd", "224", "5"),
			Tool:          profileColor(profile, "#89dceb", "117", "6"),
			Border:        profileColor(profile, "#313244", "236", "8"),
			BorderStrong:  profileColor(profile, "#45475a", "238", "8"),
			DiffHunk:      profileColor(profile, "#f2cdcd", "211", "5"),
			DiffLineNo:    profileColor(profile, "#6c7086", "242", "8"),
			Scrollbar:     profileColor(profile, "#585b70", "240", "7"),
		},
		themeSurfaces{
			Base:             surface1,
			Raised:           surface2,
			User:             userSurface,
			Composer:         surface1,
			Selection:        adaptiveTintColor(profile, background, true, [3]uint8{88, 91, 112}, [3]uint8{}, 0.35, 0, "#585b70", "", "240", ""),
			SelectionText:    profileColor(profile, "#cdd6f4", "253", "7"),
			OnAccent:         profileColor(profile, "#11111b", "233", "0"),
			DiffAdd:          adaptiveTintColor(profile, background, true, [3]uint8{166, 227, 161}, [3]uint8{}, 0.12, 0, "#223329", "", "22", ""),
			DiffAddStrong:    adaptiveTintColor(profile, background, true, [3]uint8{166, 227, 161}, [3]uint8{}, 0.22, 0, "#2a4435", "", "29", ""),
			DiffRemove:       adaptiveTintColor(profile, background, true, [3]uint8{242, 205, 205}, [3]uint8{}, 0.12, 0, "#392025", "", "52", ""),
			DiffRemoveStrong: adaptiveTintColor(profile, background, true, [3]uint8{242, 205, 205}, [3]uint8{}, 0.22, 0, "#4a2831", "", "88", ""),
		},
	)
}

func catppuccinLatteTheme(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, false, 0, 0.035, "", "#e6e9ef", "", "254")
	surface2 := adaptiveBackgroundColor(profile, background, false, 0, 0.055, "", "#dce0e8", "", "253")
	userSurface := adaptiveSurfaceTintColor(profile, background, false, [3]uint8{234, 118, 203}, 0.035, 0.04, "", "#f4ebf2", "", "255")

	return themeFrom(
		themePalette{
			Name:          "catppuccin-latte",
			IsDark:        false,
			TextPrimary:   profileColor(profile, "#4c4f69", "236", "0"),
			TextSecondary: profileColor(profile, "#5c5f77", "240", "0"),
			Muted:         profileColor(profile, "#6c6f85", "242", "8"),
			Info:          profileColor(profile, "#04a5e5", "32", "6"),
			Success:       profileColor(profile, "#327d1e", "28", "2"),
			Warning:       profileColor(profile, "#bc7200", "172", "3"),
			Danger:        profileColor(profile, "#d20f39", "160", "1"),
			Accent:        profileColor(profile, "#1e66f5", "63", "5"),
			Focus:         profileColor(profile, "#1e66f5", "63", "5"),
			UserAccent:    profileColor(profile, "#a2448f", "126", "5"),
			Tool:          profileColor(profile, "#087c9e", "30", "6"),
			Border:        profileColor(profile, "#ccd0da", "250", "8"),
			BorderStrong:  profileColor(profile, "#bcc0cc", "248", "8"),
			DiffHunk:      profileColor(profile, "#d20f39", "160", "5"),
			DiffLineNo:    profileColor(profile, "#6c6f85", "242", "8"),
			Scrollbar:     profileColor(profile, "#8c8fa1", "245", "0"),
		},
		themeSurfaces{
			Base:             surface1,
			Raised:           surface2,
			User:             userSurface,
			Composer:         surface1,
			Selection:        adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{30, 102, 245}, 0, 0.16, "", "#dce7fa", "", "153"),
			SelectionText:    profileColor(profile, "#4c4f69", "236", "0"),
			OnAccent:         profileColor(profile, "#ffffff", "255", "7"),
			DiffAdd:          adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{64, 160, 43}, 0, 0.13, "", "#e6f4ea", "", "194"),
			DiffAddStrong:    adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{64, 160, 43}, 0, 0.22, "", "#c8eccb", "", "157"),
			DiffRemove:       adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{210, 15, 57}, 0, 0.10, "", "#fdeef0", "", "224"),
			DiffRemoveStrong: adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{210, 15, 57}, 0, 0.18, "", "#f8d5da", "", "217"),
		},
	)
}
