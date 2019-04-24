package main

import (
	"log"
	"testing"
	"io/ioutil"
	"os"
	"path/filepath"
	"fmt"
	"strings"
)

const DO_CLEANUP = false
const HIDE_FIND_OUTPUT = true
const FIND_OUTPUT_FNAME = "findoutput.txt"

/*
 * The path is assumed to use '/' as the
 * path separator, and is made OS independent.
 * Any missing directory is created.
 * If there is an error the program will log
 * it and quit with a non-zero exit code.
 */
func WriteStringToFile(path string, contents string) {
	path = filepath.FromSlash(path) // Makes path OS independent
	dir,_ := filepath.Split(path) // Remove the filename

	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		log.Fatalf("Error while creating directory:\n\t%s\n", err.Error())
	}
	
	err = ioutil.WriteFile(path, []byte(contents), os.ModePerm)
	if err != nil {
		log.Fatalf("Error while writing string to file:\n\t%s\n", err.Error())
	}
}

var out = os.Stdout

func TestMain(m *testing.M) {
	// Create a small file structure which
	// will be the subject of all the tests.
	WriteStringToFile("testdir/top.txt", "firstline\n\n\n\n\n"+
	                                     "middle\n\n\n\n\n"+
	                                     "lastline")
	WriteStringToFile("testdir/left/underleft.txt", "something")
	WriteStringToFile("testdir/dontsearch.exe", "\000foo")
	WriteStringToFile("testdir/right/rightleft/bottomleft.txt", "bar")
	WriteStringToFile("testdir/right/rightright/bottomright.txt", "bar")

	var f os.File
	defer f.Close()
	if HIDE_FIND_OUTPUT {
		f, err := os.Create(FIND_OUTPUT_FNAME)
		if err != nil {
			panic(err)
		}
		out = f
	}

	code := m.Run() // Run the tests

	// Cleanup
	if DO_CLEANUP {
		err := os.RemoveAll("testdir")
		if err != nil {
			log.Fatalf("Error while removing test directory:\n"+
			           "\t%s\n"+
			           "Please remove it manually at path \".\\testdir\".\n", err.Error())
		}
		if HIDE_FIND_OUTPUT {
			err = os.Remove(FIND_OUTPUT_FNAME)
			if err != nil {
				panic(err)
			}
		}
	}
	
	os.Exit(code)
}

func ReportErrorAndExitIfNotNil(err error) {
	if err != nil {
		// Note: log.Fatal calls os.Exit(1)
		log.Fatal(err.Error())
	}
}

type Match struct {
	path    string
	match   string
	line    int
	column  int
}

func (m Match) String() string {
	return fmt.Sprintf("{%s:%d:%d: %s}", m.path, m.line, m.column, m.match)
}

func MatchMatch(m1 Match, m2 Match) bool {
	if m1.match != m2.match || m1.line != m2.line || m1.column != m2.column {
		return false
	}
	path1 := SplitPath(m1.path)
	path2 := SplitPath(m2.path)
	if len(path1) != len(path2) {
		return false
	}
	for i := range path1 {
		if path1[i] != path2[i] {
			return false
		}
	}
	return true
}

func SplitPath(path string) []string {
	return strings.Split(filepath.FromSlash(path), string(os.PathSeparator))
}

type FindParameters struct {
	paths        []string
	regex        string
	help         bool
	recursive    bool
	filter       string
	fnamesOnly   bool
	ignoreCase   bool
	quiet        bool
	verbose      bool
	noAnsiColor  bool
	noSkip       bool
}

func NewFindParameters(regex string) FindParameters {
	return FindParameters {
		DEFAULT_PATHS,
		regex,
		DEFAULT_HELP,
		DEFAULT_RECURSIVE,
		DEFAULT_FILTER,
		DEFAULT_FNAMES_ONLY,
		DEFAULT_IGNORE_CASE,
		DEFAULT_QUIET,
		DEFAULT_VERBOSE,
		DEFAULT_NO_SKIP,
		DEFAULT_NO_ANSI_COLOR,
	}
}

func Expect(t *testing.T, params FindParameters, expected []Match) {
	find(
		params.paths,
		params.regex,
		params.recursive,
		params.filter,
		params.fnamesOnly,
		params.ignoreCase,
		params.quiet,
		params.verbose,
		params.noSkip,
		params.noAnsiColor,
		out,
		func(path string, match string, line int, column int) {
			actual := Match {path, match, line, column}
			matched := false
			for i, exp := range expected {
				if MatchMatch(actual, exp) {
					expected = append(expected[:i], expected[i+1:]...)
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("Unexpected match %s\n", actual)
			}
		},
	)
	if len(expected) != 0 {
		t.Fatalf("Expected more matches %s\n", expected)
	}
}

func TestNonRecursive(t *testing.T) {
	params := NewFindParameters("some")
	params.paths = []string{"testdir/left/"}
	expected := []Match{Match{"testdir/left/underleft.txt", "some", 1, 0}}
	Expect(t, params, expected)
	
	params.regex = "dontmatchmeplease"
	expected = []Match{}
	Expect(t, params, expected)
}

func TestRecursive(t *testing.T) {
	params := NewFindParameters("bar")
	params.paths = []string{"testdir"}
	params.recursive = true
	expected := []Match {
		Match{"testdir/right/rightright/bottomright.txt", "bar", 1, 0},
		Match{"testdir/right/rightleft/bottomleft.txt", "bar", 1, 0},
	}
	Expect(t, params, expected)
}

func TestNullbytes(t *testing.T) {
	params := NewFindParameters("foo")
	params.paths = []string{"testdir/dontsearch.exe"}
	expected := []Match{}
	Expect(t, params, expected)
	
	params.noSkip = true
	expected = []Match{Match{"testdir/dontsearch.exe", "foo", 1, 1}}
	Expect(t, params, expected)
}

func TestFilenameSearch(t *testing.T) {
	params := NewFindParameters(".*right.*")
	params.paths = []string{"testdir/right"}
	params.fnamesOnly = true
	params.recursive = true
	expected := []Match {
		Match{"testdir/right/rightleft",  "rightleft",  -1, -1},
		Match{"testdir/right/rightright", "rightright", -1, -1},
		Match{"testdir/right/rightright/bottomright.txt", "bottomright.txt", -1, -1},
	}
	Expect(t, params, expected)
}

func TestIgnoreCase(t *testing.T) {
	params := NewFindParameters("LaStLiNe")
	params.paths = []string{"testdir"}
	params.ignoreCase = true
	expected := []Match {Match{"testdir/top.txt", "lastline", 11, 0}}
	Expect(t, params, expected)

	params.ignoreCase = false
	expected = []Match{}
	Expect(t, params, expected)
}

/**
 * Test ideas:
 * Filter
 * The filter should not affect the directories being followed in recursive mode. Test this.
 * Following symlinks
 * Not getting stuck in an infinite symlink loop
 * Not searching past a nullbyte
 * Searching an exe due to explicitly mentioning it (?)
 * Match multiple things in one line
 * Match at end of line (to test off-by-one)
 */
