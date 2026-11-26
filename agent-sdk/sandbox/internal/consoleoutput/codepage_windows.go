//go:build windows

package consoleoutput

import (
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

const codePageUTF8 = 65001

func DecodeConsoleOutputToUTF8(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if utf8.Valid(data) {
		return append([]byte(nil), data...), nil
	}
	codePage, err := windows.GetConsoleOutputCP()
	if err == nil && codePage != 0 && codePage != codePageUTF8 {
		if decoded, decodeErr := decodeCodePageToUTF8(codePage, data); decodeErr == nil {
			return decoded, nil
		}
	}
	ansiCodePage := windows.GetACP()
	if ansiCodePage != 0 && ansiCodePage != codePage && ansiCodePage != codePageUTF8 {
		return decodeCodePageToUTF8(ansiCodePage, data)
	}
	return nil, err
}

func decodeCodePageToUTF8(codePage uint32, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if codePage == 0 {
		codePage = windows.GetACP()
	}
	n, err := windows.MultiByteToWideChar(codePage, 0, &data[0], int32(len(data)), nil, 0)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	wide := make([]uint16, n)
	n, err = windows.MultiByteToWideChar(codePage, 0, &data[0], int32(len(data)), &wide[0], n)
	if err != nil {
		return nil, err
	}
	return []byte(string(utf16.Decode(wide[:n]))), nil
}
