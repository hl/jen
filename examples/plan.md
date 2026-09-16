# Plan: WordStats module

Build an Elixir module `WordStats` that analyzes text.

## Requirements

1. `count_words/1` — takes a binary string, returns the number of
   whitespace-separated words. Empty and whitespace-only strings return 0.
2. `frequencies/1` — takes a binary string, returns a map of each
   **downcased** word to its occurrence count. Punctuation attached to words
   should be stripped (e.g. `"hello,"` counts as `"hello"`).
3. `top/2` — takes a binary string and a non-negative integer `n`, returns
   the `n` most frequent words as a list of `{word, count}` tuples, sorted
   by count descending. Ties are broken alphabetically by word.

## Constraints

- Pure functions only; no processes, no ETS, no external dependencies.
- Functions must handle empty strings gracefully.
