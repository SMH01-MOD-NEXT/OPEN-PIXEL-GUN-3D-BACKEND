// SPDX-License-Identifier: GPL-3.0-or-later

// Command opg3d-siokey recovers the /sio payload codec key from captured
// socket.io attachments.
//
// Attachment 0 of every outgoing event is Encrypt(UTF8(eventName)), so a
// capture plus the known event name is an exact known-plaintext oracle. The
// tool sweeps candidate salts and password halves harvested from the client
// metadata and reports every combination that reproduces the plaintext.
//
// Input files are plain text, one hex blob per line; blank lines and lines
// starting with # are ignored.
//
//	salts.txt    8-byte candidates  (the PBKDF2 salt is a FieldRVA constant)
//	halves.txt   32-byte candidates (the two static password halves)
//	samples.txt  "<event> <attachment0-hex> [session-hex]" per line
//
// Example:
//
//	opg3d-siokey -salts salts.txt -halves halves.txt -samples samples.txt -pairs
package main

import (
	"bufio"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/sio"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "opg3d-siokey:", err)
		os.Exit(1)
	}
}

func run() error {
	saltsPath := flag.String("salts", "", "file of candidate PBKDF2 salts, one hex blob per line")
	halvesPath := flag.String("halves", "", "file of candidate 32-byte password halves, one hex blob per line")
	samplesPath := flag.String("samples", "", "file of captures: \"<event> <attachment0-hex> [session-hex]\" per line")
	modeList := flag.String("modes", "", "comma separated chaining modes to try (default all: cfb8,cfb128,cbc)")
	pairs := flag.Bool("pairs", false, "also sweep every staticA^staticB pair (quadratic, slower)")
	first := flag.Bool("first", false, "stop at the first match")
	flag.Parse()

	if *saltsPath == "" || *samplesPath == "" {
		flag.Usage()
		return fmt.Errorf("-salts and -samples are required")
	}

	salts, err := readBlobs(*saltsPath, sio.SaltLen)
	if err != nil {
		return err
	}
	var halves [][]byte
	if *halvesPath != "" {
		if halves, err = readBlobs(*halvesPath, sio.PasswordLen); err != nil {
			return err
		}
	}
	samples, err := readSamples(*samplesPath)
	if err != nil {
		return err
	}

	var modes []sio.Mode
	if *modeList != "" {
		for _, name := range strings.Split(*modeList, ",") {
			mode, err := sio.ParseMode(strings.TrimSpace(name))
			if err != nil {
				return err
			}
			modes = append(modes, mode)
		}
	}

	fmt.Printf("salts=%d halves=%d samples=%d pairs=%v\n", len(salts), len(halves), len(samples), *pairs)

	totalHits := 0
	for i, sample := range samples {
		started := time.Now()
		hits, stats, err := sio.Search(sio.SearchInput{
			Sample:      sample,
			Salts:       salts,
			Halves:      halves,
			Modes:       modes,
			Pairs:       *pairs,
			StopAtFirst: *first,
		})
		if err != nil {
			return fmt.Errorf("sample %d: %w", i+1, err)
		}
		fmt.Printf("sample %d/%d: plaintext=%q passwords=%d derivations=%d trials=%d elapsed=%s hits=%d\n",
			i+1, len(samples), sample.Plaintext, stats.Passwords, stats.Derivations, stats.Trials,
			time.Since(started).Round(time.Millisecond), len(hits))

		for _, hit := range hits {
			totalHits++
			matched, err := sio.Verify(hit, samples)
			if err != nil {
				return err
			}
			fmt.Printf("  HIT %s verified=%d/%d\n", hit, matched, len(samples))
		}
		if totalHits > 0 && *first {
			break
		}
	}

	if totalHits == 0 {
		fmt.Println("no candidate reproduced the expected plaintext")
		fmt.Println("the password is therefore not a pure combination of the supplied constants")
	}
	return nil
}

func readBlobs(path string, want int) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	var out [][]byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		blob, err := hex.DecodeString(text)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if want > 0 && len(blob) != want {
			continue
		}
		out = append(out, blob)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return sio.DedupeBlobs(out), nil
}

func readSamples(path string) ([]sio.Sample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	var out []sio.Sample
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) < 2 {
			return nil, fmt.Errorf("%s:%d: expected \"<event> <attachment0-hex> [session-hex]\"", path, line)
		}
		ciphertext, err := hex.DecodeString(fields[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: attachment: %w", path, line, err)
		}
		sample := sio.Sample{Ciphertext: ciphertext, Plaintext: []byte(fields[0])}
		if len(fields) > 2 {
			session, err := hex.DecodeString(fields[2])
			if err != nil {
				return nil, fmt.Errorf("%s:%d: session: %w", path, line, err)
			}
			sample.Session = session
		}
		out = append(out, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no samples", path)
	}
	return out, nil
}
