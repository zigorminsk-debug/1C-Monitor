// Package ui содержит графический интерфейс монитора на Fyne.
package ui

import (
	_ "embed"
	"image/color"
	"os"
	"path/filepath"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

//go:embed icon.png
var iconPNG []byte

// Icon — значок программы.
var Icon = fyne.NewStaticResource("icon.png", iconPNG)

// Цвета оформления (тёмная тема, как в версии на .NET).
var (
	colWindow    = color.NRGBA{0x0D, 0x14, 0x22, 0xFF}
	colCard      = color.NRGBA{0x15, 0x1F, 0x30, 0xFF}
	colGrid      = color.NRGBA{0x11, 0x1A, 0x29, 0xFF}
	colBorder    = color.NRGBA{0x27, 0x34, 0x49, 0xFF}
	colText      = color.NRGBA{0xE6, 0xED, 0xF7, 0xFF}
	colMuted     = color.NRGBA{0x8F, 0xA0, 0xB8, 0xFF}
	colDisabled  = color.NRGBA{0x5B, 0x6A, 0x82, 0xFF}
	colPrimary   = color.NRGBA{0x3B, 0x82, 0xF6, 0xFF}
	colButton    = color.NRGBA{0x22, 0x30, 0x48, 0xFF}
	colHover     = color.NRGBA{0xFF, 0xFF, 0xFF, 0x14} // полупрозрачные: рисуются поверх ячеек таблиц
	colPressed   = color.NRGBA{0xFF, 0xFF, 0xFF, 0x30}
	colSelection = color.NRGBA{0x3B, 0x82, 0xF6, 0x55}
	colRowMark   = color.NRGBA{0x1E, 0x36, 0x5E, 0xFF}
	colSuccess   = color.NRGBA{0x34, 0xD3, 0x99, 0xFF}
	colWarning   = color.NRGBA{0xFB, 0xBF, 0x24, 0xFF}
	colDanger    = color.NRGBA{0xF8, 0x71, 0x71, 0xFF}
	colScrollBar = color.NRGBA{0x3E, 0x4F, 0x6B, 0xFF}
)

type appTheme struct {
	regular, bold, italic, boldItalic, monospace fyne.Resource
}

// NewTheme создаёт тему. В Windows используются системные шрифты Segoe UI и Consolas:
// в них есть все символы интерфейса (стрелки, «≈», «Δ»); если файлов нет — встроенный шрифт Fyne.
func NewTheme() fyne.Theme {
	t := &appTheme{}
	if runtime.GOOS == "windows" {
		root := os.Getenv("WINDIR")
		if root == "" {
			root = os.Getenv("SystemRoot")
		}
		load := func(name string) fyne.Resource {
			data, err := os.ReadFile(filepath.Join(root, "Fonts", name))
			if err != nil || len(data) == 0 {
				return nil
			}
			return fyne.NewStaticResource(name, data)
		}
		t.regular, t.bold, t.italic, t.boldItalic, t.monospace = load("segoeui.ttf"), load("segoeuib.ttf"), load("segoeuii.ttf"), load("segoeuiz.ttf"), load("consola.ttf")
	}
	return t
}

func (t *appTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colWindow
	case theme.ColorNameForeground:
		return colText
	case theme.ColorNamePlaceHolder:
		return colMuted
	case theme.ColorNameDisabled:
		return colDisabled
	case theme.ColorNameButton:
		return colButton
	case theme.ColorNameDisabledButton:
		return colCard
	case theme.ColorNameHover:
		return colHover
	case theme.ColorNamePressed:
		return colPressed
	case theme.ColorNamePrimary, theme.ColorNameFocus, theme.ColorNameHyperlink:
		return colPrimary
	case theme.ColorNameSelection:
		return colSelection
	case theme.ColorNameInputBackground:
		return colGrid
	case theme.ColorNameInputBorder, theme.ColorNameSeparator:
		return colBorder
	case theme.ColorNameHeaderBackground, theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground:
		return colCard
	case theme.ColorNameScrollBar:
		return colScrollBar
	case theme.ColorNameScrollBarBackground:
		return colGrid
	case theme.ColorNameSuccess:
		return colSuccess
	case theme.ColorNameWarning:
		return colWarning
	case theme.ColorNameError:
		return colDanger
	case theme.ColorNameForegroundOnPrimary:
		return color.White
	case theme.ColorNameForegroundOnError, theme.ColorNameForegroundOnWarning, theme.ColorNameForegroundOnSuccess:
		return colWindow
	case theme.ColorNameShadow:
		return color.NRGBA{0, 0, 0, 0x66}
	}
	return theme.DefaultTheme().Color(name, theme.VariantDark)
}

func (t *appTheme) Font(style fyne.TextStyle) fyne.Resource {
	var font fyne.Resource
	switch {
	case style.Symbol:
	case style.Monospace:
		font = t.monospace
	case style.Bold && style.Italic:
		font = t.boldItalic
	case style.Bold:
		font = t.bold
	case style.Italic:
		font = t.italic
	default:
		font = t.regular
	}
	if font != nil {
		return font
	}
	return theme.DefaultTheme().Font(style)
}

func (t *appTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (t *appTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameHeadingText:
		return 22
	case theme.SizeNameInnerPadding:
		return 6
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 5
	}
	return theme.DefaultTheme().Size(name)
}
