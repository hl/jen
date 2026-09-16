ExUnit.start()
Code.require_file("lib/word_stats.ex", __DIR__)

defmodule WordStatsTest do
  use ExUnit.Case, async: true

  test "strips punctuation and combines frequencies" do
    assert WordStats.frequencies("hello, Hello! (hello) …hello…") == %{"hello" => 4}
    assert WordStats.frequencies("... () !!!") == %{}
    assert WordStats.frequencies("don't well-known") == %{"don't" => 1, "well-known" => 1}
  end

  test "empty input, counts, and ranked ties" do
    assert WordStats.count_words(" \n ") == 0
    assert WordStats.count_words("one\ttwo three") == 3
    assert WordStats.frequencies("") == %{}
    assert WordStats.top("", 3) == []
    assert WordStats.top("b! a, b a c", 2) == [{"a", 2}, {"b", 2}]
    assert WordStats.top("word", 0) == []
  end
end
