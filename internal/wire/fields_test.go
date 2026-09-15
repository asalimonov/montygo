package wire

import (
	"bufio"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const protoPath = "../../proto/monty/v1/monty.proto"

var (
	blockOpenRe = regexp.MustCompile(`^(message|enum|oneof)\s+(\w+)\s*\{\s*(\})?\s*$`)
	fieldRe     = regexp.MustCompile(`^(?:optional\s+|repeated\s+)?([\w.]+)\s+\w+\s*=\s*(\d+)\s*;$`)
)

// protoSchema maps each message of monty.proto to its fields, number to type name.
// Nested messages are named "Outer.Inner". The scanner understands nested
// messages, oneof arms, enums (skipped), optional and repeated fields, and
// ignores reserved statements.
type protoSchema map[string]map[int32]string

func loadProtoSchema(t testing.TB) protoSchema {
	t.Helper()
	f, err := os.Open(protoPath)
	require.NoError(t, err)
	defer f.Close()

	type block struct {
		kind string
		name string
	}
	var stack []block
	out := protoSchema{}
	messageName := func() (string, bool) {
		var parts []string
		for _, b := range stack {
			switch b.kind {
			case "enum":
				return "", false
			case "message":
				parts = append(parts, b.name)
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, "."), true
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := blockOpenRe.FindStringSubmatch(line); m != nil {
			kind, name, closed := m[1], m[2], m[3] != ""
			if kind == "message" {
				full := name
				if outer, ok := messageName(); ok {
					full = outer + "." + name
				}
				out[full] = map[int32]string{}
			}
			if !closed {
				stack = append(stack, block{kind: kind, name: name})
			}
			continue
		}
		if line == "}" {
			require.NotEmpty(t, stack, "unbalanced closing brace")
			stack = stack[:len(stack)-1]
			continue
		}
		if m := fieldRe.FindStringSubmatch(line); m != nil {
			name, ok := messageName()
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(m[2], 10, 32)
			require.NoError(t, err)
			require.NotContains(t, out[name], int32(n), "message %s repeats field %d", name, n)
			out[name][int32(n)] = m[1]
		}
	}
	require.NoError(t, sc.Err())
	require.Empty(t, stack, "unterminated block")
	return out
}

// resolve returns the schema name of a field type seen inside message, or ""
// for scalars and enums.
func (s protoSchema) resolve(message, typ string) string {
	if _, ok := s[message+"."+typ]; ok {
		return message + "." + typ
	}
	if _, ok := s[typ]; ok {
		return typ
	}
	return ""
}

func (s protoSchema) numbers(message string) []int32 {
	nums := make([]int32, 0, len(s[message]))
	for n := range s[message] {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	return nums
}

func TestFieldTableMatchesProto(t *testing.T) {
	schema := loadProtoSchema(t)
	require.NotEmpty(t, schema)
	for name := range schema {
		got, ok := fieldTable[name]
		require.True(t, ok, "message %s is missing from fieldTable", name)
		sorted := slices.Clone(got)
		slices.Sort(sorted)
		require.Equal(t, schema.numbers(name), sorted, "message %s: proto fields differ from fieldTable", name)
	}
	for name := range fieldTable {
		_, ok := schema[name]
		require.True(t, ok, "fieldTable names %s, which the proto does not define", name)
	}
}

func TestFieldTableIsSorted(t *testing.T) {
	for name, nums := range fieldTable {
		require.True(t, slices.IsSorted(nums), "fieldTable[%q] is not sorted", name)
		for i := 1; i < len(nums); i++ {
			require.NotEqual(t, nums[i-1], nums[i], "fieldTable[%q] repeats %d", name, nums[i])
		}
	}
}
