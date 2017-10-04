if exists("b:current_syntax")
  finish
endif

syn case match

syn keyword     skylarkStatement         return break continue lambda
syn keyword     skylarkConditional       if else elif not
syn keyword     skylarkLabel             case default
syn keyword     skylarkRepeat            for in range
syn match       skylarkDeclaration       /\<def\>/

hi def link     skylarkStatement         Statement
hi def link     skylarkConditional       Conditional
hi def link     skylarkLabel             Label
hi def link     skylarkRepeat            Repeat
hi def link     skylarkDeclaration       Type

syn keyword     skylarkCast              str type freeze bool hash int list set dict tuple

hi def link     skylarkCast              Type

syn keyword     skylarkBuiltins          load len print chr ord
syn keyword     skylarkConstants         True False Inf NaN None

hi def link     skylarkBuiltins          Keyword
hi def link     skylarkConstants         Keyword

" Comments; their contents
syn keyword     skylarkTodo              contained TODO FIXME XXX BUG
syn cluster     skylarkCommentGroup      contains=skylarkTodo
syn region      skylarkComment           start="#" end="$" contains=@skylarkCommentGroup,@Spell

hi def link     skylarkComment           Comment
hi def link     skylarkTodo              Todo

" skylark escapes
syn match       skylarkEscapeOctal       display contained "\\[0-7]\{3}"
syn match       skylarkEscapeC           display contained +\\[abfnrtv\\'"]+
syn match       skylarkEscapeX           display contained "\\x\x\{2}"
syn match       skylarkEscapeU           display contained "\\u\x\{4}"
syn match       skylarkEscapeBigU        display contained "\\U\x\{8}"
syn match       skylarkEscapeError       display contained +\\[^0-7xuUabfnrtv\\'"]+

hi def link     skylarkEscapeOctal       skylarkSpecialString
hi def link     skylarkEscapeC           skylarkSpecialString
hi def link     skylarkEscapeX           skylarkSpecialString
hi def link     skylarkEscapeU           skylarkSpecialString
hi def link     skylarkEscapeBigU        skylarkSpecialString
hi def link     skylarkSpecialString     Special
hi def link     skylarkEscapeError       Error

" Strings and their contents
syn cluster     skylarkStringGroup       contains=skylarkEscapeOctal,skylarkEscapeC,skylarkEscapeX,skylarkEscapeU,skylarkEscapeBigU,skylarkEscapeError
syn region      skylarkString            start=+"+ skip=+\\\\\|\\"+ end=+"+ contains=@skylarkStringGroup
syn region      skylarkRawString         start=+`+ end=+`+

hi def link     skylarkString            String
hi def link     skylarkRawString         String

" Characters; their contents
syn cluster     skylarkCharacterGroup    contains=skylarkEscapeOctal,skylarkEscapeC,skylarkEscapeX,skylarkEscapeU,skylarkEscapeBigU
syn region      skylarkCharacter         start=+'+ skip=+\\\\\|\\'+ end=+'+ contains=@skylarkCharacterGroup

hi def link     skylarkCharacter         Character

" Regions
syn region      skylarkBlock             start="{" end="}" transparent fold
syn region      skylarkParen             start='(' end=')' transparent

" Integers
syn match       skylarkDecimalInt        "\<\d\+\([Ee]\d\+\)\?\>"
syn match       skylarkHexadecimalInt    "\<0x\x\+\>"
syn match       skylarkOctalInt          "\<0\o\+\>"
syn match       skylarkOctalError        "\<0\o*[89]\d*\>"

hi def link     skylarkDecimalInt        Integer
hi def link     skylarkHexadecimalInt    Integer
hi def link     skylarkOctalInt          Integer
hi def link     Integer             Number

" Floating point
syn match       skylarkFloat             "\<\d\+\.\d*\([Ee][-+]\d\+\)\?\>"
syn match       skylarkFloat             "\<\.\d\+\([Ee][-+]\d\+\)\?\>"
syn match       skylarkFloat             "\<\d\+[Ee][-+]\d\+\>"

hi def link     skylarkFloat             Float
hi def link     skylarkImaginary         Number

syn sync minlines=500

let b:current_syntax = "skylark"
