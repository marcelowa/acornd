// The setup code: the one credential an install has before it has an owner.
//
// ADR 0036 §3. Until an admin exists nothing proves the person at the browser, and a console that
// answers whoever reaches the port is the window ADR 0032 left between "open by default" and "a
// door". The code closes it: `acornd start` prints it on the terminal (or into the 0600 log), it
// proves ownership when somebody claims the install, and it stops being accepted the moment an
// admin exists — the check is "no admin", not a deleted file, so a code left on disk is harmless.
package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SetupCodeFile is the file inside the install folder that holds the code. It sits beside the token
// and people.json, and ADR 0014 keeps that folder out of every sandbox mount, so an agent in a
// workspace cannot read the code that would let it claim the install.
const SetupCodeFile = "setup-code"

// setupAlphabet is Crockford's base32 without the characters people misread and retype: no I, L, O or
// U, and no 0 or 1. A code is read off a terminal and typed into a browser, often on another device,
// which is exactly the trip where those pairs go wrong.
const setupAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// setupCodeLength is eight characters in two halves of four. Thirty characters of alphabet make
// 30^8 — about 6.5e11 — guesses for a code that lives until somebody claims the install, and the
// halves make it readable off a screen.
const setupCodeLength = 8

// ErrNoSetupCode is an install with no code file: nothing has asked for one, or it was disabled.
var ErrNoSetupCode = errors.New("this install has no setup code")

// ReadSetupCode is the install's code. A missing file is ErrNoSetupCode, which the caller can print
// as it is.
func ReadSetupCode(dir string) (string, error) {
	path := filepath.Join(dir, SetupCodeFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: run `acornd setup-code`", ErrNoSetupCode)
		}
		return "", err
	}
	code := strings.TrimSpace(string(data))
	if code == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return code, nil
}

// EnsureSetupCode returns the install's code, minting one when there is none, and reports whether it
// minted one so the caller can say where it went instead of printing a credential nobody asked for.
//
// A file that exists and cannot be read is a problem to report, not a reason to mint over it: the
// running console may be about to accept exactly that code.
func EnsureSetupCode(dir string) (code string, minted bool, err error) {
	code, err = ReadSetupCode(dir)
	if err == nil {
		return code, false, nil
	}
	if !errors.Is(err, ErrNoSetupCode) {
		return "", false, err
	}
	if code, err = RotateSetupCode(dir); err != nil {
		return "", false, err
	}
	return code, true, nil
}

// RotateSetupCode replaces the install's code and returns the new one. The old code stops working at
// once, which is the answer to a code read over somebody's shoulder.
func RotateSetupCode(dir string) (string, error) {
	code, err := mintSetupCode()
	if err != nil {
		return "", err
	}
	if err := writeWhole(dir, SetupCodeFile, code+"\n"); err != nil {
		return "", err
	}
	return code, nil
}

// DisableSetupCode removes the code. It is never a lockout: the install token still signs in on the
// same form, which is why removing the code is a decision somebody may make (ADR 0036 §3).
func DisableSetupCode(dir string) error {
	if err := os.Remove(filepath.Join(dir, SetupCodeFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// MatchSetupCode compares a code somebody typed against the install's, in constant time. A missing
// file is not a match and not an error: a disabled code is refused like a wrong one, and the form
// says the same sentence either way.
func MatchSetupCode(dir, given string) (bool, error) {
	want, err := ReadSetupCode(dir)
	if err != nil {
		if errors.Is(err, ErrNoSetupCode) {
			return false, nil
		}
		return false, err
	}
	return sameSecret(strings.ToUpper(strings.TrimSpace(given)), want), nil
}

// mintSetupCode draws eight characters from the alphabet. A byte at or above the largest multiple of
// the alphabet's length is one of 240..255, and taking it modulo 30 would make the first sixteen
// characters more likely than the last fourteen; those bytes are drawn again instead.
func mintSetupCode() (string, error) {
	alphabet := len(setupAlphabet)
	limit := 256 - 256%alphabet
	letters := make([]byte, 0, setupCodeLength)
	draw := make([]byte, setupCodeLength+(setupCodeLength/2))
	for len(letters) < setupCodeLength {
		if _, err := rand.Read(draw); err != nil {
			return "", fmt.Errorf("minting a setup code: %w", err)
		}
		for _, value := range draw {
			if int(value) >= limit {
				continue
			}
			letters = append(letters, setupAlphabet[int(value)%alphabet])
			if len(letters) == setupCodeLength {
				break
			}
		}
	}
	return string(letters[:4]) + "-" + string(letters[4:]), nil
}

// writeWhole replaces a file inside the install folder with its content, 0600 and whole: written
// beside its destination and renamed onto it, so a reader never sees a half-written credential and
// the mode is never looser than 0600 — a secret created world-readable for one syscall is a secret
// that leaked.
func writeWhole(dir, name, content string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+name+"-")
	if err != nil {
		return err
	}
	// CreateTemp creates 0600 by design; the explicit chmod is for the umask on a filesystem that
	// ignores it, which is the difference between a secret and a public file.
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	_, writeErr := file.WriteString(content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(file.Name())
		return errors.Join(writeErr, closeErr)
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(file.Name())
		return err
	}
	return nil
}
