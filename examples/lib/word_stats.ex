defmodule WordStats do
  @moduledoc """
  Analyzes text: word counts, frequencies, and top-N words.
  """

  @doc "Counts whitespace-separated words in the given text."
  @spec count_words(String.t()) :: non_neg_integer()
  def count_words(text) when is_binary(text) do
    text
    |> String.split()
    |> length()
  end

  @doc "Returns a map of downcased word -> occurrence count."
  @spec frequencies(String.t()) :: %{String.t() => non_neg_integer()}
  def frequencies(text) when is_binary(text) do
    text
    |> String.downcase()
    |> String.split()
    |> Enum.map(&strip_punctuation/1)
    |> Enum.reject(&(&1 == ""))
    |> Enum.frequencies()
  end

  @doc "Returns the `n` most frequent words as `{word, count}` tuples."
  @spec top(String.t(), non_neg_integer()) :: [{String.t(), non_neg_integer()}]
  def top(text, n) when is_binary(text) and is_integer(n) and n >= 0 do
    text
    |> frequencies()
    |> Enum.sort_by(fn {word, count} -> {-count, word} end)
    |> Enum.take(n)
  end

  # Strips leading/trailing punctuation from a word.
  defp strip_punctuation(word) do
    String.replace(word, ~r/^\p{P}+|\p{P}+$/u, "")
  end
end
