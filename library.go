// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylark

// This file defines the library of built-ins.
//
// Built-ins must explicitly check the "frozen" flag before updating
// mutable types such as lists and dicts.

import (
	"bytes"
	"fmt"
	"log"
	"math/big"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/skylark/syntax"
)

// Universe defines the set of universal built-ins, such as None, True, and len.
//
// The Go application may add or remove items from the
// universe dictionary before Skylark evaluation begins.
// All values in the dictionary must be immutable.
// Skylark programs cannot modify the dictionary.
var Universe StringDict

func init() {
	// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#XYZ
	Universe = StringDict{
		"None":      None,
		"True":      True,
		"False":     False,
		"any":       NewBuiltin("any", any),
		"all":       NewBuiltin("all", all),
		"bool":      NewBuiltin("bool", bool_),
		"chr":       NewBuiltin("chr", chr),
		"cmp":       NewBuiltin("cmp", cmp),
		"dict":      NewBuiltin("dict", dict),
		"dir":       NewBuiltin("dir", dir),
		"enumerate": NewBuiltin("enumerate", enumerate),
		"float":     NewBuiltin("float", float),   // requires resolve.AllowFloat
		"freeze":    NewBuiltin("freeze", freeze), // requires resolve.AllowFreeze
		"getattr":   NewBuiltin("getattr", getattr),
		"hasattr":   NewBuiltin("hasattr", hasattr),
		"hash":      NewBuiltin("hash", hash),
		"int":       NewBuiltin("int", int_),
		"len":       NewBuiltin("len", len_),
		"list":      NewBuiltin("list", list),
		"max":       NewBuiltin("max", minmax),
		"min":       NewBuiltin("min", minmax),
		"ord":       NewBuiltin("ord", ord),
		"print":     NewBuiltin("print", print),
		"range":     NewBuiltin("range", range_),
		"repr":      NewBuiltin("repr", repr),
		"reversed":  NewBuiltin("reversed", reversed),
		"set":       NewBuiltin("set", set), // requires resolve.AllowSet
		"sorted":    NewBuiltin("sorted", sorted),
		"str":       NewBuiltin("str", str),
		"tuple":     NewBuiltin("tuple", tuple),
		"type":      NewBuiltin("type", type_),
		"zip":       NewBuiltin("zip", zip),
	}
}

type builtinMethod func(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error)

// methods of built-in types
var (
	// See https://bazel.build/versions/master/docs/skylark/lib/dict.html.
	dictMethods = map[string]builtinMethod{
		"clear":      dict_clear,
		"get":        dict_get,
		"items":      dict_items,
		"keys":       dict_keys,
		"pop":        dict_pop,
		"popitem":    dict_popitem,
		"setdefault": dict_setdefault,
		"update":     dict_update,
		"values":     dict_values,
	}

	// See https://bazel.build/versions/master/docs/skylark/lib/list.html.
	listMethods = map[string]builtinMethod{
		"append": list_append,
		"clear":  list_clear,
		"extend": list_extend,
		"index":  list_index,
		"insert": list_insert,
		"pop":    list_pop,
		"remove": list_remove,
	}

	// See https://bazel.build/versions/master/docs/skylark/lib/string.html.
	stringMethods = map[string]builtinMethod{
		"bytes":            string_iterable,
		"capitalize":       string_capitalize,
		"codepoints":       string_iterable,
		"count":            string_count,
		"endswith":         string_endswith,
		"find":             string_find,
		"format":           string_format,
		"index":            string_index,
		"isalnum":          string_isalnum,
		"isalpha":          string_isalpha,
		"isdigit":          string_isdigit,
		"islower":          string_islower,
		"isspace":          string_isspace,
		"istitle":          string_istitle,
		"isupper":          string_isupper,
		"join":             string_join,
		"lower":            string_lower,
		"lstrip":           string_strip, // sic
		"partition":        string_partition,
		"replace":          string_replace,
		"rfind":            string_rfind,
		"rindex":           string_rindex,
		"rpartition":       string_partition, // sic
		"rsplit":           string_split,     // sic
		"rstrip":           string_strip,     // sic
		"split":            string_split,
		"splitlines":       string_splitlines,
		"split_bytes":      string_iterable, // sic
		"split_codepoints": string_iterable, // sic
		"startswith":       string_startswith,
		"strip":            string_strip,
		"title":            string_title,
		"upper":            string_upper,
	}

	// See https://bazel.build/versions/master/docs/skylark/lib/set.html.
	setMethods = map[string]builtinMethod{
		"union": set_union,
	}
)

func builtinMethodOf(recv Value, name string) builtinMethod {
	switch recv.(type) {
	case String:
		return stringMethods[name]
	case *List:
		return listMethods[name]
	case *Dict:
		return dictMethods[name]
	case *Set:
		return setMethods[name]
	}
	return nil
}

func builtinAttr(recv Value, name string, methods map[string]builtinMethod) (Value, error) {
	method := methods[name]
	if method == nil {
		return nil, nil // no such method
	}

	// Allocate a closure over 'method'.
	impl := func(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		return method(b.Name(), b.Receiver(), args, kwargs)
	}
	return NewBuiltin(name, impl).BindReceiver(recv), nil
}

func builtinAttrNames(methods map[string]builtinMethod) []string {
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// UnpackArgs unpacks the positional and keyword arguments into the
// supplied parameter variables.  pairs is an alternating list of names
// and pointers to variables.
//
// If the variable is a bool, int, string, *List, *Dict, Callable,
// Iterable, or user-defined implementation of Value,
// UnpackArgs performs the appropriate type check.
// (An int uses the AsInt32 check.)
// If the parameter name ends with "?",
// it and all following parameters are optional.
//
// If the variable implements Value, UnpackArgs may call
// its Type() method while constructing the error message.
//
// Beware: an optional *List, *Dict, Callable, Iterable, or Value variable that is
// not assigned is not a valid Skylark Value, so the caller must
// explicitly handle such cases by interpreting nil as None or some
// computed default.
func UnpackArgs(fnname string, args Tuple, kwargs []Tuple, pairs ...interface{}) error {
	nparams := len(pairs) / 2
	var defined intset
	defined.init(nparams)

	// positional arguments
	if len(args) > nparams {
		return fmt.Errorf("%s: got %d arguments, want at most %d",
			fnname, len(args), nparams)
	}
	for i, arg := range args {
		defined.set(i)
		if err := unpackOneArg(arg, pairs[2*i+1]); err != nil {
			return fmt.Errorf("%s: for parameter %d: %s", fnname, i+1, err)
		}
	}

	// keyword arguments
kwloop:
	for _, item := range kwargs {
		name, arg := item[0].(String), item[1]
		for i := 0; i < nparams; i++ {
			paramName := pairs[2*i].(string)
			if paramName[len(paramName)-1] == '?' {
				paramName = paramName[:len(paramName)-1]
			}
			if paramName == string(name) {
				// found it
				if defined.set(i) {
					return fmt.Errorf("%s: got multiple values for keyword argument %s",
						fnname, name)
				}
				ptr := pairs[2*i+1]
				if err := unpackOneArg(arg, ptr); err != nil {
					return fmt.Errorf("%s: for parameter %s: %s", fnname, name, err)
				}
				continue kwloop
			}
		}
		return fmt.Errorf("%s: unexpected keyword argument %s", fnname, name)
	}

	// Check that all non-optional parameters are defined.
	// (We needn't check the first len(args).)
	for i := len(args); i < nparams; i++ {
		name := pairs[2*i].(string)
		if strings.HasSuffix(name, "?") {
			break // optional
		}
		if !defined.get(i) {
			return fmt.Errorf("%s: missing argument for %s", fnname, name)
		}
	}

	return nil
}

// UnpackPositionalArgs unpacks the positional arguments into
// corresponding variables.  Each element of vars is a pointer; see
// UnpackArgs for allowed types and conversions.
//
// UnpackPositionalArgs reports an error if the number of arguments is
// less than min or greater than len(vars), if kwargs is nonempty, or if
// any conversion fails.
func UnpackPositionalArgs(fnname string, args Tuple, kwargs []Tuple, min int, vars ...interface{}) error {
	if len(kwargs) > 0 {
		return fmt.Errorf("%s: unexpected keyword arguments", fnname)
	}
	max := len(vars)
	if len(args) < min {
		var atleast string
		if min < max {
			atleast = "at least "
		}
		return fmt.Errorf("%s: got %d arguments, want %s%d", fnname, len(args), atleast, min)
	}
	if len(args) > max {
		var atmost string
		if max > min {
			atmost = "at most "
		}
		return fmt.Errorf("%s: got %d arguments, want %s%d", fnname, len(args), atmost, max)
	}
	for i, arg := range args {
		if err := unpackOneArg(arg, vars[i]); err != nil {
			return fmt.Errorf("%s: for parameter %d: %s", fnname, i+1, err)
		}
	}
	return nil
}

func unpackOneArg(v Value, ptr interface{}) error {
	ok := true
	switch ptr := ptr.(type) {
	case *Value:
		*ptr = v
	case *string:
		*ptr, ok = AsString(v)
		if !ok {
			return fmt.Errorf("got %s, want string", v.Type())
		}
	case *bool:
		*ptr = bool(v.Truth())
	case *int:
		var err error
		*ptr, err = AsInt32(v)
		if err != nil {
			return err
		}
	case **List:
		*ptr, ok = v.(*List)
		if !ok {
			return fmt.Errorf("got %s, want list", v.Type())
		}
	case **Dict:
		*ptr, ok = v.(*Dict)
		if !ok {
			return fmt.Errorf("got %s, want dict", v.Type())
		}
	case *Callable:
		*ptr, ok = v.(Callable)
		if !ok {
			return fmt.Errorf("got %s, want callable", v.Type())
		}
	case *Iterable:
		*ptr, ok = v.(Iterable)
		if !ok {
			return fmt.Errorf("got %s, want iterable", v.Type())
		}
	default:
		param := reflect.ValueOf(ptr).Elem()
		if !reflect.TypeOf(v).AssignableTo(param.Type()) {
			// Detect mistakes by caller.
			if !param.Type().AssignableTo(reflect.TypeOf(new(Value)).Elem()) {
				log.Fatalf("internal error: invalid ptr type: %T", ptr)
			}
			// Assume it's safe to call Type() on a zero instance.
			paramType := param.Interface().(Value).Type()
			return fmt.Errorf("got %s, want %s", v.Type(), paramType)
		}
		param.Set(reflect.ValueOf(v))
	}
	return nil
}

// ---- builtin functions ----

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#all
func all(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("all", args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var x Value
	for iter.Next(&x) {
		if !x.Truth() {
			return False, nil
		}
	}
	return True, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#any
func any(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("all", args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var x Value
	for iter.Next(&x) {
		if x.Truth() {
			return True, nil
		}
	}
	return False, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#bool
func bool_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value = False
	if err := UnpackPositionalArgs("bool", args, kwargs, 0, &x); err != nil {
		return nil, err
	}
	return x.Truth(), nil
}

func chr(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("chr does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("chr: got %d arguments, want 1", len(args))
	}
	i, err := AsInt32(args[0])
	if err != nil {
		return nil, fmt.Errorf("chr: got %s, want int", args[0].Type())
	}
	if i < 0 {
		return nil, fmt.Errorf("chr: Unicode code point %d out of range (<0)", i)
	}
	if i > unicode.MaxRune {
		return nil, fmt.Errorf("chr: Unicode code point U+%X out of range (>0x10FFFF)", i)
	}
	return String(string(i)), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#cmp
func cmp(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("cmp does not accept keyword arguments")
	}
	if len(args) != 2 {
		return nil, fmt.Errorf("cmp: got %d arguments, want 2", len(args))
	}
	x := args[0]
	y := args[1]
	if lt, err := Compare(syntax.LT, x, y); err != nil {
		return nil, err
	} else if lt {
		return MakeInt(+1), nil // x < y
	}
	if gt, err := Compare(syntax.GT, x, y); err != nil {
		return nil, err
	} else if gt {
		return MakeInt(-1), nil // x > y
	}
	return zero, nil // x == y or one of the operands is NaN
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#dict
func dict(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("dict: got %d arguments, want at most 1", len(args))
	}
	dict := new(Dict)
	if err := updateDict(dict, args, kwargs); err != nil {
		return nil, fmt.Errorf("dict: %v", err)
	}
	return dict, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#dir
func dir(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("dir does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("dir: got %d arguments, want 1", len(args))
	}

	var names []string
	if x, ok := args[0].(HasAttrs); ok {
		names = x.AttrNames()
	}
	elems := make([]Value, len(names))
	for i, name := range names {
		elems[i] = String(name)
	}
	return NewList(elems), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#enumerate
func enumerate(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	var start int
	if err := UnpackPositionalArgs("enumerate", args, kwargs, 1, &iterable, &start); err != nil {
		return nil, err
	}

	iter := iterable.Iterate()
	if iter == nil {
		return nil, fmt.Errorf("enumerate: got %s, want iterable", iterable.Type())
	}
	defer iter.Done()

	var pairs []Value
	var x Value

	if n := Len(iterable); n >= 0 {
		// common case: known length
		pairs = make([]Value, 0, n)
		array := make(Tuple, 2*n) // allocate a single backing array
		for i := 0; iter.Next(&x); i++ {
			pair := array[:2:2]
			array = array[2:]
			pair[0] = MakeInt(start + i)
			pair[1] = x
			pairs = append(pairs, pair)
		}
	} else {
		// non-sequence (unknown length)
		for i := 0; iter.Next(&x); i++ {
			pair := Tuple{MakeInt(start + i), x}
			pairs = append(pairs, pair)
		}
	}

	return NewList(pairs), nil
}

func float(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("float does not accept keyword arguments")
	}
	if len(args) == 0 {
		return Float(0.0), nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("float got %d arguments, wants 1", len(args))
	}
	switch x := args[0].(type) {
	case Bool:
		if x {
			return Float(1.0), nil
		} else {
			return Float(0.0), nil
		}
	case Int:
		return x.Float(), nil
	case Float:
		return x, nil
	case String:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return nil, err
		}
		return Float(f), nil
	default:
		return nil, fmt.Errorf("float got %s, want number or string", x.Type())
	}
}

func freeze(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("freeze does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("freeze got %d arguments, wants 1", len(args))
	}
	args[0].Freeze()
	return args[0], nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#getattr
func getattr(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var object, dflt Value
	var name string
	if err := UnpackPositionalArgs("getattr", args, kwargs, 2, &object, &name, &dflt); err != nil {
		return nil, err
	}
	if o, ok := object.(HasAttrs); ok {
		if v, err := o.Attr(name); v != nil || err != nil {
			return v, err
		}
	}
	if dflt != nil {
		return dflt, nil
	}
	return nil, fmt.Errorf("%s has no .%s field or method", object.Type(), name)
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#hasattr
func hasattr(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var object Value
	var name string
	if err := UnpackPositionalArgs("hasattr", args, kwargs, 2, &object, &name); err != nil {
		return nil, err
	}
	if object, ok := object.(HasAttrs); ok {
		if v, err := object.Attr(name); v != nil || err != nil {
			return True, nil
		}
	}
	return False, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#hash
func hash(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	if err := UnpackPositionalArgs("hash", args, kwargs, 1, &x); err != nil {
		return nil, err
	}
	h, err := x.Hash()
	return MakeUint(uint(h)), err
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#int
func int_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value = zero
	var base Value
	if err := UnpackArgs("int", args, kwargs, "x?", &x, "base?", &base); err != nil {
		return nil, err
	}

	// "If x is not a number or base is given, x must be a string."
	if s, ok := AsString(x); ok {
		b := 10
		if base != nil {
			var err error
			b, err = AsInt32(base)
			if err != nil || b != 0 && (b < 2 || b > 36) {
				return nil, fmt.Errorf("int: base must be an integer >= 2 && <= 36")
			}
		}

		orig := s // save original for error message

		if len(s) > 1 {
			var sign string
			i := 0
			if s[0] == '+' || s[0] == '-' {
				sign = s[:1]
				i++
			}

			if i < len(s) && s[i] == '0' {
				hasbase := 0
				if i+2 < len(s) {
					switch s[i+1] {
					case 'o', 'O':
						// SetString doesn't understand "0o755"
						// so modify s to "0755".
						// Octals are rare, so allocation is fine.
						s = sign + "0" + s[i+2:]
						hasbase = 8
					case 'x', 'X':
						hasbase = 16
					}

					if hasbase != 0 && b != 0 {
						// Explicit base doesn't match prefix,
						// e.g. int("0o755", 16).
						if hasbase != b {
							goto invalid
						}

						// SetString requires base=0
						// if there's a base prefix.
						b = 0
					}
				}

				// For automatic base detection,
				// a string starting with zero
				// must be all zeros.
				// Thus we reject "0755".
				if hasbase == 0 && b == 0 {
					for ; i < len(s); i++ {
						if s[i] != '0' {
							goto invalid
						}
					}
				}
			}
		}

		// NOTE: int(x) permits arbitrary precision, unlike the scanner.
		if i, ok := new(big.Int).SetString(s, b); ok {
			return Int{i}, nil
		}

	invalid:
		return nil, fmt.Errorf("int: invalid literal with base %d: %s", b, orig)
	}

	if base != nil {
		return nil, fmt.Errorf("int: can't convert non-string with explicit base")
	}
	i, err := ConvertToInt(x)
	if err != nil {
		return nil, fmt.Errorf("int: %s", err)
	}
	return i, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#len
func len_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	if err := UnpackPositionalArgs("len", args, kwargs, 1, &x); err != nil {
		return nil, err
	}
	len := Len(x)
	if len < 0 {
		return nil, fmt.Errorf("value of type %s has no len", x.Type())
	}
	return MakeInt(len), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#list
func list(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("list", args, kwargs, 0, &iterable); err != nil {
		return nil, err
	}
	var elems []Value
	if iterable != nil {
		iter := iterable.Iterate()
		defer iter.Done()
		if n := Len(iterable); n > 0 {
			elems = make([]Value, 0, n) // preallocate if length known
		}
		var x Value
		for iter.Next(&x) {
			elems = append(elems, x)
		}
	}
	return NewList(elems), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#min
func minmax(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s requires at least one positional argument", fn.Name())
	}
	var keyFunc Callable
	if err := UnpackArgs(fn.Name(), nil, kwargs, "key?", &keyFunc); err != nil {
		return nil, err
	}
	var op syntax.Token
	if fn.Name() == "max" {
		op = syntax.GT
	} else {
		op = syntax.LT
	}
	var iterable Value
	if len(args) == 1 {
		iterable = args[0]
	} else {
		iterable = args
	}
	iter := Iterate(iterable)
	if iter == nil {
		return nil, fmt.Errorf("%s: %s value is not iterable", fn.Name(), iterable.Type())
	}
	defer iter.Done()
	var extremum Value
	if !iter.Next(&extremum) {
		return nil, fmt.Errorf("%s: argument is an empty sequence", fn.Name())
	}

	var extremeKey Value
	var keyargs Tuple
	if keyFunc == nil {
		extremeKey = extremum
	} else {
		keyargs = Tuple{extremum}
		res, err := Call(thread, keyFunc, keyargs, nil)
		if err != nil {
			return nil, err
		}
		extremeKey = res
	}

	var x Value
	for iter.Next(&x) {
		var key Value
		if keyFunc == nil {
			key = x
		} else {
			keyargs[0] = x
			res, err := Call(thread, keyFunc, keyargs, nil)
			if err != nil {
				return nil, err
			}
			key = res
		}

		if ok, err := Compare(op, key, extremeKey); err != nil {
			return nil, err
		} else if ok {
			extremum = x
			extremeKey = key
		}
	}
	return extremum, nil
}

func ord(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("ord does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("ord: got %d arguments, want 1", len(args))
	}
	s, ok := AsString(args[0])
	if !ok {
		return nil, fmt.Errorf("ord: got %s, want string", args[0].Type())
	}
	r, sz := utf8.DecodeRuneInString(s)
	if sz == 0 || sz != len(s) {
		n := utf8.RuneCountInString(s)
		return nil, fmt.Errorf("ord: string encodes %d Unicode code points, want 1", n)
	}
	return MakeInt(int(r)), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#print
func print(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var buf bytes.Buffer
	path := make([]Value, 0, 4)
	sep := ""
	for _, v := range args {
		buf.WriteString(sep)
		if s, ok := AsString(v); ok {
			buf.WriteString(s)
		} else {
			writeValue(&buf, v, path)
		}
		sep = " "
	}
	for _, pair := range kwargs {
		buf.WriteString(sep)
		buf.WriteString(string(pair[0].(String)))
		buf.WriteString("=")
		if s, ok := AsString(pair[1]); ok {
			buf.WriteString(s)
		} else {
			writeValue(&buf, pair[1], path)
		}
		sep = " "
	}

	if thread.Print != nil {
		thread.Print(thread, buf.String())
	} else {
		fmt.Fprintln(os.Stderr, &buf)
	}
	return None, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#range
func range_(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var start, stop, step int
	step = 1
	if err := UnpackPositionalArgs("range", args, kwargs, 1, &start, &stop, &step); err != nil {
		return nil, err
	}
	list := new(List)
	switch len(args) {
	case 1:
		// range(stop)
		start, stop = 0, start
		fallthrough
	case 2:
		// range(start, stop)
		for i := start; i < stop; i += step {
			list.elems = append(list.elems, MakeInt(i))
		}
	case 3:
		// range(start, stop, step)
		if step == 0 {
			return nil, fmt.Errorf("range: step argument must not be zero")
		}
		if step > 0 {
			for i := start; i < stop; i += step {
				list.elems = append(list.elems, MakeInt(i))
			}
		} else {
			for i := start; i >= stop; i += step {
				list.elems = append(list.elems, MakeInt(i))
			}
		}
	}
	return list, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#repr
func repr(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var x Value
	if err := UnpackPositionalArgs("repr", args, kwargs, 1, &x); err != nil {
		return nil, err
	}
	return String(x.String()), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#reversed.
func reversed(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("reversed", args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var elems []Value
	if n := Len(args[0]); n >= 0 {
		elems = make([]Value, 0, n) // preallocate if length known
	}
	var x Value
	for iter.Next(&x) {
		elems = append(elems, x)
	}
	n := len(elems)
	for i := 0; i < n>>1; i++ {
		elems[i], elems[n-1-i] = elems[n-1-i], elems[i]
	}
	return NewList(elems), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#set
func set(thread *Thread, fn *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("set", args, kwargs, 0, &iterable); err != nil {
		return nil, err
	}
	set := new(Set)
	if iterable != nil {
		iter := iterable.Iterate()
		defer iter.Done()
		var x Value
		for iter.Next(&x) {
			if err := set.Insert(x); err != nil {
				return nil, err
			}
		}
	}
	return set, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#sorted
func sorted(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	var cmp Callable
	var reverse bool
	if err := UnpackArgs("sorted", args, kwargs,
		"iterable", &iterable,
		"cmp?", &cmp,
		"reverse?", &reverse,
	); err != nil {
		return nil, err
	}

	iter := iterable.Iterate()
	defer iter.Done()
	var elems []Value
	if n := Len(iterable); n > 0 {
		elems = make(Tuple, 0, n) // preallocate if length is known
	}
	var x Value
	for iter.Next(&x) {
		elems = append(elems, x)
	}
	slice := &sortSlice{thread: thread, elems: elems, cmp: cmp}
	if reverse {
		sort.Sort(sort.Reverse(slice))
	} else {
		sort.Sort(slice)
	}
	return NewList(slice.elems), slice.err
}

type sortSlice struct {
	thread *Thread
	elems  []Value
	cmp    Callable
	err    error
	pair   [2]Value
}

func (s *sortSlice) Len() int { return len(s.elems) }
func (s *sortSlice) Less(i, j int) bool {
	x, y := s.elems[i], s.elems[j]
	if s.cmp != nil {
		// Strange things will happen if cmp fails, or returns a non-int.
		s.pair[0], s.pair[1] = x, y // avoid allocation
		res, err := Call(s.thread, s.cmp, Tuple(s.pair[:]), nil)
		if err != nil {
			s.err = err
		}
		cmp, ok := res.(Int)
		return ok && cmp.Sign() < 0
	}
	ok, err := Compare(syntax.LT, x, y)
	if err != nil {
		s.err = err
	}
	return ok
}
func (s *sortSlice) Swap(i, j int) {
	s.elems[i], s.elems[j] = s.elems[j], s.elems[i]
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#str
func str(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("str does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("str: got %d arguments, want exactly 1", len(args))
	}
	x := args[0]
	if _, ok := AsString(x); !ok {
		x = String(x.String())
	}
	return x, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#tuple
func tuple(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs("tuple", args, kwargs, 0, &iterable); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return Tuple(nil), nil
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var elems Tuple
	if n := Len(iterable); n > 0 {
		elems = make(Tuple, 0, n) // preallocate if length is known
	}
	var x Value
	for iter.Next(&x) {
		elems = append(elems, x)
	}
	return elems, nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#type
func type_(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("type does not accept keyword arguments")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("type: got %d arguments, want exactly 1", len(args))
	}
	return String(args[0].Type()), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/globals.html#zip
func zip(thread *Thread, _ *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("zip does not accept keyword arguments")
	}
	rows, cols := 0, len(args)
	iters := make([]Iterator, cols)
	for i, seq := range args {
		it := Iterate(seq)
		if it == nil {
			return nil, fmt.Errorf("zip: argument #%d is not iterable: %s", i+1, seq.Type())
		}
		iters[i] = it
		n := Len(seq)
		if n < 0 {
			// TODO(adonovan): support iterables of unknown length.
			return nil, fmt.Errorf("zip: argument #%d has unknown length", i+1)
		}
		if i == 0 || n < rows {
			rows = n
		}
	}
	result := make([]Value, rows)
	array := make(Tuple, cols*rows) // allocate a single backing array
	for i := 0; i < rows; i++ {
		tuple := array[:cols:cols]
		array = array[cols:]
		for j, iter := range iters {
			iter.Next(&tuple[j])
		}
		result[i] = tuple
	}
	return NewList(result), nil
}

// ---- methods of built-in types ---

// https://docs.python.org/2/library/stdtypes.html#dict.get
func dict_get(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	var key, dflt Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &key, &dflt); err != nil {
		return nil, err
	}
	if v, ok, err := recv.(*Dict).Get(key); err != nil {
		return nil, err
	} else if ok {
		return v, nil
	} else if dflt != nil {
		return dflt, nil
	}
	return None, nil
}

// https://docs.python.org/2/library/stdtypes.html#dict.clear
func dict_clear(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return None, recv.(*Dict).Clear()
}

// https://docs.python.org/2/library/stdtypes.html#dict.items
func dict_items(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	items := recv.(*Dict).Items()
	res := make([]Value, len(items))
	for i, item := range items {
		res[i] = item // convert [2]Value to Value
	}
	return NewList(res), nil
}

// https://docs.python.org/2/library/stdtypes.html#dict.keys
func dict_keys(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return NewList(recv.(*Dict).Keys()), nil
}

// https://docs.python.org/2/library/stdtypes.html#dict.pop
func dict_pop(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := recv_.(*Dict)
	var k, d Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &k, &d); err != nil {
		return nil, err
	}
	if v, found, err := recv.Delete(k); err != nil {
		return nil, err // dict is frozen or key is unhashable
	} else if found {
		return v, nil
	} else if d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("pop: missing key")
}

// https://docs.python.org/2/library/stdtypes.html#dict.popitem
func dict_popitem(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := recv_.(*Dict)
	k, ok := recv.ht.first()
	if !ok {
		return nil, fmt.Errorf("popitem: empty dict")
	}
	v, _, err := recv.Delete(k)
	if err != nil {
		return nil, err // dict is frozen
	}
	return Tuple{k, v}, nil
}

// https://docs.python.org/2/library/stdtypes.html#dict.setdefault
func dict_setdefault(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	var key, dflt Value = nil, None
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &key, &dflt); err != nil {
		return nil, err
	}
	dict := recv.(*Dict)
	if v, ok, err := dict.Get(key); err != nil {
		return nil, err
	} else if ok {
		return v, nil
	} else {
		return dflt, dict.Set(key, dflt)
	}
}

// https://docs.python.org/2/library/stdtypes.html#dict.update
func dict_update(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("update: got %d arguments, want at most 1", len(args))
	}
	if err := updateDict(recv.(*Dict), args, kwargs); err != nil {
		return nil, fmt.Errorf("update: %v", err)
	}
	return None, nil
}

// https://docs.python.org/2/library/stdtypes.html#dict.update
func dict_values(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	items := recv.(*Dict).Items()
	res := make([]Value, len(items))
	for i, item := range items {
		res[i] = item[1]
	}
	return NewList(res), nil
}

// https://docs.python.org/2/library/stdtypes.html#list.append
func list_append(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := recv_.(*List)
	var object Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &object); err != nil {
		return nil, err
	}
	if err := recv.checkMutable("append to", true); err != nil {
		return nil, err
	}
	recv.elems = append(recv.elems, object)
	return None, nil
}

// https://docs.python.org/2/library/stdtypes.html#list.clear
func list_clear(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return None, recv_.(*List).Clear()
}

// https://docs.python.org/2/library/stdtypes.html#list.extend
func list_extend(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := recv_.(*List)
	var iterable Iterable
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	if err := recv.checkMutable("extend", true); err != nil {
		return nil, err
	}
	listExtend(recv, iterable)
	return None, nil
}

// https://docs.python.org/2/library/stdtypes.html#list.index
func list_index(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := recv_.(*List)
	var value, start_, end_ Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &value, &start_, &end_); err != nil {
		return nil, err
	}

	start, end, err := indices(start_, end_, recv.Len())
	if err != nil {
		return nil, fmt.Errorf("%s: %s", fnname, err)
	}

	for i := start; i < end; i++ {
		if eq, err := Equal(recv.elems[i], value); err != nil {
			return nil, fmt.Errorf("index: %s", err)
		} else if eq {
			return MakeInt(i), nil
		}
	}
	return nil, fmt.Errorf("index: value not in list")
}

// https://docs.python.org/2/library/stdtypes.html#list.insert
func list_insert(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := recv_.(*List)
	var index int
	var object Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 2, &index, &object); err != nil {
		return nil, err
	}
	if err := recv.checkMutable("insert into", true); err != nil {
		return nil, err
	}

	if index < 0 {
		index += recv.Len()
	}

	if index >= recv.Len() {
		// end
		recv.elems = append(recv.elems, object)
	} else {
		if index < 0 {
			index = 0 // start
		}
		recv.elems = append(recv.elems, nil)
		copy(recv.elems[index+1:], recv.elems[index:]) // slide up one
		recv.elems[index] = object
	}
	return None, nil
}

// https://docs.python.org/2/library/stdtypes.html#list.remove
func list_remove(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := recv_.(*List)
	var value Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &value); err != nil {
		return nil, err
	}
	if err := recv.checkMutable("remove from", true); err != nil {
		return nil, err
	}
	for i, elem := range recv.elems {
		if eq, err := Equal(elem, value); err != nil {
			return nil, fmt.Errorf("remove: %v", err)
		} else if eq {
			recv.elems = append(recv.elems[:i], recv.elems[i+1:]...)
			return None, nil
		}
	}
	return nil, fmt.Errorf("remove: element not found")
}

// https://docs.python.org/2/library/stdtypes.html#list.pop
func list_pop(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	list := recv.(*List)
	index := list.Len() - 1
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0, &index); err != nil {
		return nil, err
	}
	if index < 0 || index >= list.Len() {
		return nil, fmt.Errorf("pop: index %d is out of range [0:%d]", index, list.Len())
	}
	if err := list.checkMutable("pop from", true); err != nil {
		return nil, err
	}
	res := list.elems[index]
	list.elems = append(list.elems[:index], list.elems[index+1:]...)
	return res, nil
}

// https://docs.python.org/2/library/stdtypes.html#str.capitalize
func string_capitalize(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return String(strings.Title(string(recv.(String)))), nil
}

// string_iterable returns an unspecified iterable value whose iterator yields:
// - bytes: numeric values of successive bytes
// - codepoints: numeric values of successive Unicode code points
// - split_bytes: successive 1-byte substrings
// - split_codepoints: successive substrings that encode a single Unicode code point.
func string_iterable(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return stringIterable{
		s:          recv.(String),
		split:      fnname[0] == 's',
		codepoints: fnname[len(fnname)-2] == 't',
	}, nil
}

// https://docs.python.org/2/library/stdtypes.html#str.count
func string_count(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(recv_.(String))

	var sub string
	var start_, end_ Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &sub, &start_, &end_); err != nil {
		return nil, err
	}

	start, end, err := indices(start_, end_, len(recv))
	if err != nil {
		return nil, fmt.Errorf("%s: %s", fnname, err)
	}

	var slice string
	if start < end {
		slice = recv[start:end]
	}
	return MakeInt(strings.Count(slice, sub)), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.endswith
func string_endswith(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(recv_.(String))
	var suffix string
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &suffix); err != nil {
		return nil, err
	}
	return Bool(strings.HasSuffix(recv, suffix)), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.isalnum
func string_isalnum(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))
	for _, r := range recv {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.isalpha
func string_isalpha(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))
	for _, r := range recv {
		if !unicode.IsLetter(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.isdigit
func string_isdigit(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))
	for _, r := range recv {
		if !unicode.IsDigit(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.islower
func string_islower(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))
	return Bool(isCasedString(recv) && recv == strings.ToLower(recv)), nil
}

// isCasedString reports whether its argument contains any cased characters.
func isCasedString(s string) bool {
	for _, r := range s {
		if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || unicode.SimpleFold(r) != r {
			return true
		}
	}
	return false
}

// https://docs.python.org/2/library/stdtypes.html#str.isspace
func string_isspace(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))
	for _, r := range recv {
		if !unicode.IsSpace(r) {
			return False, nil
		}
	}
	return Bool(recv != ""), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.istitle
func string_istitle(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))

	// Python semantics differ from x==strings.{To,}Title(x) in Go:
	// "uppercase characters may only follow uncased characters and
	// lowercase characters only cased ones."
	var cased, prevCased bool
	for _, r := range recv {
		if unicode.IsUpper(r) {
			if prevCased {
				return False, nil
			}
			cased = true
			prevCased = true
		} else if unicode.IsLower(r) {
			if !prevCased {
				return False, nil
			}
			prevCased = true
			cased = true
		} else {
			prevCased = false
		}
	}
	return Bool(cased), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.isupper
func string_isupper(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))
	return Bool(isCasedString(recv) && recv == strings.ToUpper(recv)), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.find
func string_find(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(fnname, string(recv.(String)), args, kwargs, true, false)
}

// https://docs.python.org/2/library/stdtypes.html#str.format
func string_format(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	format := string(recv_.(String))
	var auto, manual bool // kinds of positional indexing used
	path := make([]Value, 0, 4)
	var buf bytes.Buffer
	index := 0
	for {
		// TODO(adonovan): replace doubled "}}" with "}" and reject single '}'.
		i := strings.IndexByte(format, '{')
		if i < 0 {
			buf.WriteString(format)
			break
		}
		buf.WriteString(format[:i])

		if i+1 < len(format) && format[i+1] == '{' {
			// "{{" means a literal '{'
			buf.WriteByte('{')
			format = format[i+2:]
			continue
		}

		format = format[i+1:]
		i = strings.IndexByte(format, '}')
		if i < 0 {
			return nil, fmt.Errorf("unmatched '{' in format")
		}

		var arg Value
		conv := "s"
		var spec string

		field := format[:i]
		format = format[i+1:]

		var name string
		if i := strings.IndexByte(field, '!'); i < 0 {
			// "name" or "name:spec"
			if i := strings.IndexByte(field, ':'); i < 0 {
				name = field
			} else {
				name = field[:i]
				spec = field[i+1:]
			}
		} else {
			// "name!conv" or "name!conv:spec"
			name = field[:i]
			field = field[i+1:]
			// "conv" or "conv:spec"
			if i := strings.IndexByte(field, ':'); i < 0 {
				conv = field
			} else {
				conv = field[:i]
				spec = field[i+1:]
			}
		}

		if name == "" {
			// "{}": automatic indexing
			if manual {
				return nil, fmt.Errorf("cannot switch from manual field specification to automatic field numbering")
			}
			auto = true
			if index >= len(args) {
				return nil, fmt.Errorf("tuple index out of range")
			}
			arg = args[index]
			index++
		} else if num, err := strconv.Atoi(name); err == nil {
			// positional argument
			if auto {
				return nil, fmt.Errorf("cannot switch from automatic field numbering to manual field specification")
			}
			manual = true
			if num >= len(args) {
				return nil, fmt.Errorf("tuple index out of range")
			} else {
				arg = args[num]
			}
		} else {
			// keyword argument
			for _, kv := range kwargs {
				if string(kv[0].(String)) == name {
					arg = kv[1]
					break
				}
			}
			if arg == nil {
				// Skylark does not support Python's x.y or a[i] syntaxes.
				if strings.Contains(name, ".") {
					return nil, fmt.Errorf("attribute syntax x.y is not supported in replacement fields: %s", name)
				}
				if strings.Contains(name, "[") {
					return nil, fmt.Errorf("element syntax a[i] is not supported in replacement fields: %s", name)
				}
				return nil, fmt.Errorf("keyword %s not found", name)
			}
		}

		if spec != "" {
			// Skylark does not support Python's format_spec features.
			return nil, fmt.Errorf("format spec features not supported in replacement fields: %s", spec)
		}

		switch conv {
		case "s":
			if str, ok := AsString(arg); ok {
				buf.WriteString(str)
			} else {
				writeValue(&buf, arg, path)
			}
		case "r":
			writeValue(&buf, arg, path)
		default:
			return nil, fmt.Errorf("unknown conversion %q", conv)
		}
	}
	return String(buf.String()), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.index
func string_index(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(fnname, string(recv.(String)), args, kwargs, false, false)
}

// https://docs.python.org/2/library/stdtypes.html#str.join
func string_join(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(recv_.(String))
	var iterable Iterable
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var buf bytes.Buffer
	var x Value
	for i := 0; iter.Next(&x); i++ {
		if i > 0 {
			buf.WriteString(recv)
		}
		s, ok := AsString(x)
		if !ok {
			return nil, fmt.Errorf("in list, want string, got %s", x.Type())
		}
		buf.WriteString(s)
	}
	return String(buf.String()), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.lower
func string_lower(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return String(strings.ToLower(string(recv.(String)))), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.lstrip
func string_lstrip(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return String(strings.TrimLeftFunc(string(recv.(String)), unicode.IsSpace)), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.partition
func string_partition(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(recv_.(String))
	var sep string
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &sep); err != nil {
		return nil, err
	}
	if sep == "" {
		return nil, fmt.Errorf("%s: empty separator", fnname)
	}
	var i int
	if fnname[0] == 'p' {
		i = strings.Index(recv, sep) // partition
	} else {
		i = strings.LastIndex(recv, sep) // rpartition
	}
	tuple := make(Tuple, 0, 3)
	if i < 0 {
		if fnname[0] == 'p' {
			tuple = append(tuple, String(recv), String(""), String(""))
		} else {
			tuple = append(tuple, String(""), String(""), String(recv))
		}
	} else {
		tuple = append(tuple, String(recv[:i]), String(sep), String(recv[i+len(sep):]))
	}
	return tuple, nil
}

// https://docs.python.org/2/library/stdtypes.html#str.replace
func string_replace(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(recv_.(String))
	var old, new string
	count := -1
	if err := UnpackPositionalArgs(fnname, args, kwargs, 2, &old, &new, &count); err != nil {
		return nil, err
	}
	return String(strings.Replace(recv, old, new, count)), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.rfind
func string_rfind(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(fnname, string(recv.(String)), args, kwargs, true, true)
}

// https://docs.python.org/2/library/stdtypes.html#str.rindex
func string_rindex(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	return string_find_impl(fnname, string(recv.(String)), args, kwargs, false, true)
}

// https://docs.python.org/2/library/stdtypes.html#str.rstrip
func string_rstrip(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return String(strings.TrimRightFunc(string(recv.(String)), unicode.IsSpace)), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.startswith
func string_startswith(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(recv_.(String))
	var prefix string
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &prefix); err != nil {
		return nil, err
	}
	return Bool(strings.HasPrefix(recv, prefix)), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.strip
// https://docs.python.org/2/library/stdtypes.html#str.lstrip
// https://docs.python.org/2/library/stdtypes.html#str.rstrip
func string_strip(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	var chars string
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0, &chars); err != nil {
		return nil, err
	}
	recv := string(recv_.(String))
	var s string
	switch fnname[0] {
	case 's': // strip
		if chars != "" {
			s = strings.Trim(recv, chars)
		} else {
			s = strings.TrimSpace(recv)
		}
	case 'l': // lstrip
		if chars != "" {
			s = strings.TrimLeft(recv, chars)
		} else {
			s = strings.TrimLeftFunc(recv, unicode.IsSpace)
		}
	case 'r': // rstrip
		if chars != "" {
			s = strings.TrimRight(recv, chars)
		} else {
			s = strings.TrimRightFunc(recv, unicode.IsSpace)
		}
	}
	return String(s), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.title
func string_title(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return String(strings.Title(strings.ToLower(string(recv.(String))))), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.upper
func string_upper(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0); err != nil {
		return nil, err
	}
	return String(strings.ToUpper(string(recv.(String)))), nil
}

// https://docs.python.org/2/library/stdtypes.html#str.split
// https://docs.python.org/2/library/stdtypes.html#str.rsplit
func string_split(fnname string, recv_ Value, args Tuple, kwargs []Tuple) (Value, error) {
	recv := string(recv_.(String))
	var sep_ Value
	maxsplit := -1
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0, &sep_, &maxsplit); err != nil {
		return nil, err
	}

	var res []string

	if sep_ == nil || sep_ == None {
		// special case: split on whitespace
		if maxsplit == 0 {
			res = append(res, recv)
		} else if maxsplit < 0 {
			res = strings.Fields(recv)
		} else if fnname == "split" {
			res = splitspace(recv, maxsplit+1)
		} else { // rsplit
			// TODO(adonovan): implement.
			return nil, fmt.Errorf("rsplit(None, %d): maxsplit > 0 not yet supported", maxsplit)
		}

	} else if sep, ok := AsString(sep_); ok {
		if sep == "" {
			return nil, fmt.Errorf("split: empty separator")
		}
		// usual case: split on non-empty separator
		if maxsplit == 0 {
			res = append(res, recv)
		} else if maxsplit < 0 {
			res = strings.Split(recv, sep)
		} else if fnname == "split" {
			res = strings.SplitN(recv, sep, maxsplit+1)
		} else { // rsplit
			res = strings.Split(recv, sep)
			if excess := len(res) - maxsplit; excess > 0 {
				res[0] = strings.Join(res[:excess], sep)
				res = append(res[:1], res[excess:]...)
			}
		}

	} else {
		return nil, fmt.Errorf("split: got %s for separator, want string", sep_.Type())
	}

	list := make([]Value, len(res))
	for i, x := range res {
		list[i] = String(x)
	}
	return NewList(list), nil
}

func splitspace(s string, max int) []string {
	var res []string
	start := -1 // index of field start, or -1 in a region of spaces
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				if len(res)+1 == max {
					break // let this field run to the end
				}
				res = append(res, s[start:i])
				start = -1
			}
		} else if start == -1 {
			start = i
		}
	}
	if start >= 0 {
		res = append(res, s[start:])
	}
	return res
}

// https://docs.python.org/2/library/stdtypes.html#str.splitlines
func string_splitlines(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	var keepends bool
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0, &keepends); err != nil {
		return nil, err
	}
	s := string(recv.(String))
	var lines []string
	// TODO(adonovan): handle CRLF correctly.
	if keepends {
		lines = strings.SplitAfter(s, "\n")
	} else {
		lines = strings.Split(s, "\n")
	}
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	list := make([]Value, len(lines))
	for i, x := range lines {
		list[i] = String(x)
	}
	return NewList(list), nil
}

// See https://bazel.build/versions/master/docs/skylark/lib/set.html#union.
func set_union(fnname string, recv Value, args Tuple, kwargs []Tuple) (Value, error) {
	var iterable Iterable
	if err := UnpackPositionalArgs(fnname, args, kwargs, 0, &iterable); err != nil {
		return nil, err
	}
	iter := iterable.Iterate()
	defer iter.Done()
	union, err := recv.(*Set).Union(iter)
	if err != nil {
		return nil, fmt.Errorf("union: %v", err)
	}
	return union, nil
}

// Common implementation of string_{r}{find,index}.
func string_find_impl(fnname string, s string, args Tuple, kwargs []Tuple, allowError, last bool) (Value, error) {
	var sub string
	var start_, end_ Value
	if err := UnpackPositionalArgs(fnname, args, kwargs, 1, &sub, &start_, &end_); err != nil {
		return nil, err
	}

	start, end, err := indices(start_, end_, len(s))
	if err != nil {
		return nil, fmt.Errorf("%s: %s", fnname, err)
	}
	var slice string
	if start < end {
		slice = s[start:end]
	}

	var i int
	if last {
		i = strings.LastIndex(slice, sub)
	} else {
		i = strings.Index(slice, sub)
	}
	if i < 0 {
		if !allowError {
			return nil, fmt.Errorf("substring not found")
		}
		return MakeInt(-1), nil
	}
	return MakeInt(i + start), nil
}

// Common implementation of builtin dict function and dict.update method.
// Precondition: len(updates) == 0 or 1.
func updateDict(dict *Dict, updates Tuple, kwargs []Tuple) error {
	if len(updates) == 1 {
		switch updates := updates[0].(type) {
		case NoneType:
			// no-op
		case *Dict:
			// Iterate over dict's key/value pairs, not just keys.
			for _, item := range updates.Items() {
				if err := dict.Set(item[0], item[1]); err != nil {
					return err // dict is frozen
				}
			}
		default:
			// all other sequences
			iter := Iterate(updates)
			if iter == nil {
				return fmt.Errorf("got %s, want iterable", updates.Type())
			}
			defer iter.Done()
			var pair Value
			for i := 0; iter.Next(&pair); i++ {
				iter2 := Iterate(pair)
				if iter2 == nil {
					return fmt.Errorf("dictionary update sequence element #%d is not iterable (%s)", i, pair.Type())

				}
				defer iter2.Done()
				len := Len(pair)
				if len < 0 {
					return fmt.Errorf("dictionary update sequence element #%d has unknown length (%s)", i, pair.Type())
				} else if len != 2 {
					return fmt.Errorf("dictionary update sequence element #%d has length %d, want 2", i, len)
				}
				var k, v Value
				iter2.Next(&k)
				iter2.Next(&v)
				if err := dict.Set(k, v); err != nil {
					return err
				}
			}
		}
	}

	// Then add the kwargs.
	for _, pair := range kwargs {
		if err := dict.Set(pair[0], pair[1]); err != nil {
			return err // dict is frozen
		}
	}

	return nil
}
