package tuikit

import (
	"image/color"

	"github.com/charmbracelet/colorprofile"
)

func defaultAdaptiveThemeVariant(profile colorprofile.Profile, dark bool, background color.Color) Theme {
	if dark {
		return adaptiveDarkThemeVariant(profile, background)
	}
	return adaptiveLightThemeVariant(profile, background)
}

func adaptiveDarkThemeVariant(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, true, 0.08, 0, "#11111b", "", "233", "")
	surface2 := adaptiveBackgroundColor(profile, background, true, 0.12, 0, "#181825", "", "234", "")
	selection := adaptiveTintColor(profile, background, true, [3]uint8{88, 91, 112}, [3]uint8{}, 0.35, 0, "#585b70", "", "240", "")
	userBg := adaptiveBackgroundColor(profile, background, true, 0.08, 0, "#181825", "", "234", "")
	addBg := adaptiveTintColor(profile, background, true, [3]uint8{166, 227, 161}, [3]uint8{}, 0.12, 0, "#223329", "", "22", "")
	addStrongBg := adaptiveTintColor(profile, background, true, [3]uint8{166, 227, 161}, [3]uint8{}, 0.22, 0, "#2a4435", "", "29", "")
	delBg := adaptiveTintColor(profile, background, true, [3]uint8{242, 205, 205}, [3]uint8{}, 0.12, 0, "#392025", "", "52", "")
	delStrongBg := adaptiveTintColor(profile, background, true, [3]uint8{242, 205, 205}, [3]uint8{}, 0.22, 0, "#4a2831", "", "88", "")

	return Theme{
		Name:             "dark",
		IsDark:           true,
		AppBg:            nil,
		PanelBorder:      profileColor(profile, "#313244", "236", "8"),
		PanelTitle:       profileColor(profile, "#cdd6f4", "253", "7"),
		TextPrimary:      profileColor(profile, "#cdd6f4", "253", "7"),
		TextSecondary:    profileColor(profile, "#a6adc8", "248", "7"),
		SecondaryText:    profileColor(profile, "#a6adc8", "248", "7"),
		MutedText:        profileColor(profile, "#6c7086", "242", "8"),
		Info:             profileColor(profile, "#89dceb", "117", "6"),
		Success:          profileColor(profile, "#a6e3a1", "114", "2"),
		Warning:          profileColor(profile, "#f9e2af", "221", "3"),
		Error:            profileColor(profile, "#f38ba8", "204", "1"),
		Accent:           profileColor(profile, "#cba6f7", "183", "5"),
		Focus:            profileColor(profile, "#b4befe", "147", "6"),
		ModalBg:          surface1,
		StatusBg:         surface1,
		StatusText:       profileColor(profile, "#a6adc8", "248", "7"),
		CommandBg:        nil,
		CommandActive:    selection,
		CommandText:      profileColor(profile, "#cdd6f4", "253", "7"),
		CommandSubText:   profileColor(profile, "#6c7086", "242", "8"),
		SelectionFg:      profileColor(profile, "#cdd6f4", "253", "7"),
		SelectionBg:      selection,
		InputSelectionFg: profileColor(profile, "#11111b", "233", "0"),
		InputSelectionBg: profileColor(profile, "#b4befe", "147", "6"),

		AssistantFg:        profileColor(profile, "#cdd6f4", "253", "7"),
		ReasoningFg:        profileColor(profile, "#7f849c", "243", "8"),
		UserFg:             profileColor(profile, "#cdd6f4", "253", "7"),
		UserBg:             userBg,
		UserPrefixFg:       profileColor(profile, "#f2cdcd", "224", "5"),
		UserMentionFg:      profileColor(profile, "#f2cdcd", "211", "5"),
		ToolFg:             profileColor(profile, "#89dceb", "117", "6"),
		DiffAddFg:          profileColor(profile, "#a6e3a1", "114", "2"),
		DiffRemoveFg:       profileColor(profile, "#f38ba8", "211", "1"),
		DiffHeaderFg:       profileColor(profile, "#a6adc8", "248", "7"),
		DiffHunkFg:         profileColor(profile, "#f2cdcd", "211", "5"),
		DiffAddBg:          addBg,
		DiffAddStrongBg:    addStrongBg,
		DiffRemoveBg:       delBg,
		DiffRemoveStrongBg: delStrongBg,
		DiffLineNoFg:       profileColor(profile, "#6c7086", "242", "8"),
		DiffGutterFg:       profileColor(profile, "#a6adc8", "248", "8"),
		DiffPanelBorder:    profileColor(profile, "#313244", "236", "8"),
		SectionFg:          profileColor(profile, "#cdd6f4", "253", "7"),
		KeyLabelFg:         profileColor(profile, "#a6adc8", "248", "7"),
		NoteFg:             profileColor(profile, "#89dceb", "116", "8"),
		PromptFg:           profileColor(profile, "#b4befe", "147", "5"),
		CursorFg:           profileColor(profile, "#f5e0dc", "254", "7"),
		ScrollHintFg:       profileColor(profile, "#f9e2af", "221", "3"),

		InputBarBg:          nil,
		InputBarFg:          profileColor(profile, "#cdd6f4", "253", "7"),
		ToolOutputBg:        nil,
		HelpHintFg:          profileColor(profile, "#6c7086", "242", "8"),
		SpinnerFg:           profileColor(profile, "#b4befe", "147", "5"),
		SeparatorFg:         profileColor(profile, "#45475a", "238", "8"),
		RoleBorderFg:        profileColor(profile, "#313244", "236", "8"),
		NewMsgBg:            selection,
		ComposerBorder:      profileColor(profile, "#313244", "236", "8"),
		ComposerBorderFocus: profileColor(profile, "#b4befe", "147", "5"),
		ScrollbarTrack:      profileColor(profile, "#181825", "234", "8"),
		ScrollbarThumb:      profileColor(profile, "#585b70", "240", "7"),
		LinkFg:              profileColor(profile, "#89dceb", "117", "6"),
		CodeBg:              surface2,
		CodeBlockFg:         profileColor(profile, "#cdd6f4", "253", "7"),
		CodeBlockBg:         surface1,
		TranscriptRail:      profileColor(profile, "#313244", "236", "8"),
		TranscriptShell:     profileColor(profile, "#45475a", "238", "8"),
		TranscriptPillBg:    nil,
		CodeSurface:         surface1,
		TableHeaderBg:       surface1,
		TableBorder:         profileColor(profile, "#313244", "236", "8"),
	}
}

func adaptiveLightThemeVariant(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, false, 0, 0.035, "", "#e6e9ef", "", "254")
	surface2 := adaptiveBackgroundColor(profile, background, false, 0, 0.055, "", "#dce0e8", "", "253")
	selection := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{92, 95, 119}, 0, 0.22, "", "#5c5f77", "", "240")
	userBg := adaptiveBackgroundColor(profile, background, false, 0, 0.035, "", "#e6e9ef", "", "254")
	addBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{64, 160, 43}, 0, 0.13, "", "#e6f4ea", "", "194")
	addStrongBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{64, 160, 43}, 0, 0.22, "", "#c8eccb", "", "157")
	delBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{210, 15, 57}, 0, 0.10, "", "#fdeef0", "", "224")
	delStrongBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{210, 15, 57}, 0, 0.18, "", "#f8d5da", "", "217")

	return Theme{
		Name:             "light",
		IsDark:           false,
		AppBg:            nil,
		PanelBorder:      profileColor(profile, "#ccd0da", "250", "8"),
		PanelTitle:       profileColor(profile, "#4c4f69", "236", "0"),
		TextPrimary:      profileColor(profile, "#4c4f69", "236", "0"),
		TextSecondary:    profileColor(profile, "#5c5f77", "240", "0"),
		SecondaryText:    profileColor(profile, "#5c5f77", "240", "0"),
		MutedText:        profileColor(profile, "#6c6f85", "242", "8"),
		Info:             profileColor(profile, "#04a5e5", "32", "6"),
		Success:          profileColor(profile, "#40a02b", "28", "2"),
		Warning:          profileColor(profile, "#bc7200", "172", "3"),
		Error:            profileColor(profile, "#d20f39", "160", "1"),
		Accent:           profileColor(profile, "#7287fd", "93", "5"),
		Focus:            profileColor(profile, "#1e66f5", "63", "5"),
		ModalBg:          surface1,
		StatusBg:         surface1,
		StatusText:       profileColor(profile, "#5c5f77", "240", "0"),
		CommandBg:        nil,
		CommandActive:    selection,
		CommandText:      profileColor(profile, "#4c4f69", "236", "0"),
		CommandSubText:   profileColor(profile, "#9ca0b0", "244", "8"),
		SelectionFg:      profileColor(profile, "#eff1f5", "254", "7"),
		SelectionBg:      selection,
		InputSelectionFg: profileColor(profile, "#ffffff", "255", "7"),
		InputSelectionBg: profileColor(profile, "#1e66f5", "63", "5"),

		AssistantFg:        profileColor(profile, "#4c4f69", "236", "0"),
		ReasoningFg:        profileColor(profile, "#6c6f85", "242", "8"),
		UserFg:             profileColor(profile, "#4c4f69", "236", "0"),
		UserBg:             userBg,
		UserPrefixFg:       profileColor(profile, "#ea76cb", "160", "5"),
		UserMentionFg:      profileColor(profile, "#ea76cb", "160", "5"),
		ToolFg:             profileColor(profile, "#04a5e5", "32", "6"),
		DiffAddFg:          profileColor(profile, "#327d1e", "28", "2"),
		DiffRemoveFg:       profileColor(profile, "#d20f39", "160", "1"),
		DiffHeaderFg:       profileColor(profile, "#5c5f77", "240", "0"),
		DiffHunkFg:         profileColor(profile, "#d20f39", "160", "5"),
		DiffAddBg:          addBg,
		DiffAddStrongBg:    addStrongBg,
		DiffRemoveBg:       delBg,
		DiffRemoveStrongBg: delStrongBg,
		DiffLineNoFg:       profileColor(profile, "#9ca0b0", "244", "8"),
		DiffGutterFg:       profileColor(profile, "#5c5f77", "240", "8"),
		DiffPanelBorder:    profileColor(profile, "#ccd0da", "250", "8"),
		SectionFg:          profileColor(profile, "#4c4f69", "236", "0"),
		KeyLabelFg:         profileColor(profile, "#5c5f77", "240", "0"),
		NoteFg:             profileColor(profile, "#04a5e5", "32", "8"),
		PromptFg:           profileColor(profile, "#1e66f5", "63", "5"),
		CursorFg:           profileColor(profile, "#4c4f69", "236", "0"),
		ScrollHintFg:       profileColor(profile, "#bc7200", "172", "3"),

		InputBarBg:          nil,
		InputBarFg:          profileColor(profile, "#4c4f69", "236", "0"),
		ToolOutputBg:        nil,
		HelpHintFg:          profileColor(profile, "#9ca0b0", "244", "8"),
		SpinnerFg:           profileColor(profile, "#1e66f5", "63", "5"),
		SeparatorFg:         profileColor(profile, "#bcc0cc", "248", "8"),
		RoleBorderFg:        profileColor(profile, "#ccd0da", "250", "8"),
		NewMsgBg:            selection,
		ComposerBorder:      profileColor(profile, "#ccd0da", "250", "8"),
		ComposerBorderFocus: profileColor(profile, "#1e66f5", "63", "5"),
		ScrollbarTrack:      profileColor(profile, "#e6e9ef", "254", "8"),
		ScrollbarThumb:      profileColor(profile, "#acb0be", "248", "7"),
		LinkFg:              profileColor(profile, "#04a5e5", "32", "6"),
		CodeBg:              surface2,
		CodeBlockFg:         profileColor(profile, "#4c4f69", "236", "0"),
		CodeBlockBg:         surface1,
		TranscriptRail:      profileColor(profile, "#ccd0da", "250", "8"),
		TranscriptShell:     profileColor(profile, "#bcc0cc", "248", "8"),
		TranscriptPillBg:    nil,
		CodeSurface:         surface1,
		TableHeaderBg:       surface1,
		TableBorder:         profileColor(profile, "#ccd0da", "250", "8"),
	}
}
