package main

import (
    "log"
    "testing"
    "io/ioutil"
    "os"
    "path/filepath"
    "fmt"
    "strings"
    "io"
)

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
 * Test whether giving mutually exclusive flags to gos returns an error.
 */

const (
    DoCleanup   = false
    OutputFname = "gos_output.txt"
    TempTestDir = "testdir"
)

/*
 * The path is assumed to use the '/' path
 * separator. Missing directories are
 * created. Files are overwritten if they
 * already exist. Panic if there's an error.
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

var out io.Writer

func TestMain(m *testing.M) {
    // This small file structure will be the testing environment.
    WriteStringToFile(TempTestDir+"/top.txt", "firstline\n\n\n\n\n"+
                                              "middle\n\n\n\n\n"+
                                              "lastline")
    WriteStringToFile(TempTestDir+"/left/underleft.txt", "something")
    WriteStringToFile(TempTestDir+"/dontsearch.exe", "\000foo")
    WriteStringToFile(TempTestDir+"/right/rightleft/bottomleft.txt", "bar")
    WriteStringToFile(TempTestDir+"/right/rightright/bottomright.txt", "bar")

    f, err := os.Create(OutputFname)
    if err != nil {
        panic(err)
    }
    defer f.Close()
    out = f

    code := m.Run() // Run the tests

    // Cleanup
    if DoCleanup {
        err := os.RemoveAll(TempTestDir)
        if err != nil {
            log.Fatalf("Could not remove the test directory \"%s\","+
                       "please remove it manually.", TempTestDir)
        }
        f.Close()
        err = os.Remove(OutputFname)
        if err != nil {
            panic(err)
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

func Expect(t *testing.T, params FindParameters, expected []Match) {
    params.Out = out
    params.Listener = func(path string, match string, row int, column int) {
        actual := Match {path, match, row, column}
        matched := false
        for i, exp := range expected {
            if MatchMatch(actual, exp) {
                // Remove the match from the expected list
                expected = append(expected[:i], expected[i+1:]...)
                matched = true
                break
            }
        }
        if !matched {
            t.Fatalf("Unexpected match %s\n", actual)
        }
    }
    find(params)
    if len(expected) != 0 {
        t.Fatalf("Expected more matches %s\n", expected)
    }
}

func TestNonRecursive(t *testing.T) {
    params := NewFindParameters("some")
    params.Paths = []string{TempTestDir+"/left/"}
    expected := []Match{Match{TempTestDir+"/left/underleft.txt", "some", 1, 0}}
    Expect(t, params, expected)
    
    params.RegexString = "dontmatchmeplease"
    expected = []Match{}
    Expect(t, params, expected)
}

func TestRecursive(t *testing.T) {
    params := NewFindParameters("bar")
    params.Paths = []string{TempTestDir}
    params.Recursive = true
    expected := []Match {
        Match{TempTestDir+"/right/rightright/bottomright.txt", "bar", 1, 0},
        Match{TempTestDir+"/right/rightleft/bottomleft.txt", "bar", 1, 0},
    }
    Expect(t, params, expected)
}

func TestNullbytes(t *testing.T) {
    params := NewFindParameters("foo")
    params.Paths = []string{TempTestDir+"/dontsearch.exe"}
    expected := []Match{}
    Expect(t, params, expected)
    
    params.NoSkip = true
    expected = []Match{Match{TempTestDir+"/dontsearch.exe", "foo", 1, 1}}
    Expect(t, params, expected)
}

func TestFilenameSearch(t *testing.T) {
    params := NewFindParameters(".*right.*")
    params.Paths = []string{TempTestDir+"/right"}
    params.FnamesOnly = true
    params.Recursive = true
    expected := []Match {
        Match{TempTestDir+"/right/rightleft",  "rightleft",  -1, -1},
        Match{TempTestDir+"/right/rightright", "rightright", -1, -1},
        Match{TempTestDir+"/right/rightright/bottomright.txt", "bottomright.txt", -1, -1},
    }
    Expect(t, params, expected)
}

func TestIgnoreCase(t *testing.T) {
    params := NewFindParameters("LaStLiNe")
    params.Paths = []string{TempTestDir}
    params.IgnoreCase = true
    expected := []Match {Match{TempTestDir+"/top.txt", "lastline", 11, 0}}
    Expect(t, params, expected)

    params.IgnoreCase = false
    expected = []Match{}
    Expect(t, params, expected)
}
