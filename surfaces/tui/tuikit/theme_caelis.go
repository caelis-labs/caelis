package tuikit

import (
	"image/color"

	"github.com/charmbracelet/colorprofile"
)

func defaultAdaptiveThemeVariant(profile colorprofile.Profile, dark bool, background color.Color) Theme {
	if dark {
		return caelisDuskTheme(profile, background)
	}
	return caelisDawnTheme(profile, background)
}

func caelisDuskTheme(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, true, 0.06, 0, "#171a21", "", "234", "")
	surface2 := adaptiveBackgroundColor(profile, background, true, 0.10, 0, "#1d222c", "", "235", "")
	userSurface := adaptiveSurfaceTintColor(profile, background, true, [3]uint8{211, 154, 140}, 0.06, 0.05, "#1d191a", "", "235", "")

	return themeFrom(
		themePalette{
			Name:          "caelis-dusk",
			IsDark:        true,
			TextPrimary:   profileColor(profile, "#e7e9ee", "254", "7"),
			TextSecondary: profileColor(profile, "#a8afbd", "248", "7"),
			Muted:         profileColor(profile, "#9da5b6", "247", "8"),
			Info:          profileColor(profile, "#7c9cf5", "111", "4"),
			Success:       profileColor(profile, "#7fb58a", "108", "2"),
			Warning:       profileColor(profile, "#d4a65a", "179", "3"),
			Danger:        profileColor(profile, "#e1848c", "174", "1"),
			Accent:        profileColor(profile, "#7c9cf5", "111", "4"),
			Focus:         profileColor(profile, "#7c9cf5", "111", "4"),
			UserAccent:    profileColor(profile, "#d39a8c", "174", "3"),
			Tool:          profileColor(profile, "#65b8b0", "73", "6"),
			Border:        profileColor(profile, "#434c5e", "239", "8"),
			BorderStrong:  profileColor(profile, "#526079", "240", "8"),
			DiffHunk:      profileColor(profile, "#9aafe9", "111", "4"),
			DiffLineNo:    profileColor(profile, "#7f8899", "244", "8"),
			DiffGutter:    profileColor(profile, "#979fb0", "246", "8"),
			Scrollbar:     profileColor(profile, "#71829d", "244", "7"),
		},
		themeSurfaces{
			Base:             surface1,
			Raised:           surface2,
			User:             userSurface,
			Composer:         surface1,
			Selection:        adaptiveTintColor(profile, background, true, [3]uint8{124, 156, 245}, [3]uint8{}, 0.28, 0, "#2a3a5c", "", "24", ""),
			SelectionText:    profileColor(profile, "#e7e9ee", "254", "7"),
			OnAccent:         profileColor(profile, "#11151d", "233", "0"),
			DiffAdd:          adaptiveTintColor(profile, background, true, [3]uint8{127, 181, 138}, [3]uint8{}, 0.12, 0, "#1e3025", "", "22", ""),
			DiffAddStrong:    adaptiveTintColor(profile, background, true, [3]uint8{127, 181, 138}, [3]uint8{}, 0.22, 0, "#294334", "", "29", ""),
			DiffRemove:       adaptiveTintColor(profile, background, true, [3]uint8{225, 132, 140}, [3]uint8{}, 0.12, 0, "#351f24", "", "52", ""),
			DiffRemoveStrong: adaptiveTintColor(profile, background, true, [3]uint8{225, 132, 140}, [3]uint8{}, 0.22, 0, "#482a31", "", "88", ""),
		},
	)
}

func caelisDawnTheme(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, false, 0, 0.03, "", "#f5f6f7", "", "254")
	surface2 := adaptiveBackgroundColor(profile, background, false, 0, 0.06, "", "#eff1f3", "", "253")
	userSurface := adaptiveSurfaceTintColor(profile, background, false, [3]uint8{169, 95, 87}, 0.03, 0.04, "", "#f6f2f1", "", "255")

	return themeFrom(
		themePalette{
			Name:          "caelis-dawn",
			IsDark:        false,
			TextPrimary:   profileColor(profile, "#242a35", "235", "0"),
			TextSecondary: profileColor(profile, "#505a6b", "240", "0"),
			Muted:         profileColor(profile, "#5f697b", "241", "8"),
			Info:          profileColor(profile, "#315fbb", "25", "4"),
			Success:       profileColor(profile, "#2f7d48", "28", "2"),
			Warning:       profileColor(profile, "#9a6418", "130", "3"),
			Danger:        profileColor(profile, "#b73a4a", "160", "1"),
			Accent:        profileColor(profile, "#315fbb", "25", "4"),
			Focus:         profileColor(profile, "#315fbb", "25", "4"),
			UserAccent:    profileColor(profile, "#a95f57", "130", "1"),
			Tool:          profileColor(profile, "#0f766e", "30", "6"),
			Border:        profileColor(profile, "#cbd1dc", "251", "8"),
			BorderStrong:  profileColor(profile, "#aeb7c6", "249", "8"),
			DiffLineNo:    profileColor(profile, "#768093", "244", "8"),
			DiffGutter:    profileColor(profile, "#667184", "242", "8"),
			Scrollbar:     profileColor(profile, "#7485a5", "244", "0"),
		},
		themeSurfaces{
			Base:             surface1,
			Raised:           surface2,
			User:             userSurface,
			Composer:         surface1,
			Selection:        adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{49, 95, 187}, 0, 0.16, "", "#dce7fa", "", "153"),
			SelectionText:    profileColor(profile, "#242a35", "235", "0"),
			OnAccent:         profileColor(profile, "#ffffff", "255", "7"),
			DiffAdd:          adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{47, 125, 72}, 0, 0.12, "", "#e8f3eb", "", "194"),
			DiffAddStrong:    adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{47, 125, 72}, 0, 0.21, "", "#cfe6d5", "", "157"),
			DiffRemove:       adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{183, 58, 74}, 0, 0.10, "", "#f9ecee", "", "224"),
			DiffRemoveStrong: adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{183, 58, 74}, 0, 0.18, "", "#f0d6da", "", "217"),
		},
	)
}

func adaptiveSurfaceTintColor(
	profile colorprofile.Profile,
	terminal color.Color,
	dark bool,
	tint [3]uint8,
	surfaceAlpha float64,
	tintAlpha float64,
	darkFallback string,
	lightFallback string,
	dark256 string,
	light256 string,
) color.Color {
	if profile == colorprofile.TrueColor {
		if r, g, b, ok := rgb8(terminal); ok {
			surfaceTop := [3]uint8{}
			if dark {
				surfaceTop = [3]uint8{255, 255, 255}
			}
			surface := blendRGB([3]uint8{r, g, b}, surfaceTop, surfaceAlpha)
			return profileColor(profile, hexColor(blendRGB(surface, tint, tintAlpha)), "", "")
		}
		if dark {
			return profileColor(profile, darkFallback, "", "")
		}
		return profileColor(profile, lightFallback, "", "")
	}
	if profile == colorprofile.ANSI256 {
		if dark {
			return profileColor(profile, "", dark256, "")
		}
		return profileColor(profile, "", light256, "")
	}
	return nil
}
