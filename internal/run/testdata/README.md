# testdata

`real.diff` is the output of `git diff <base>..HEAD` from git 2.43.0, kept so the scanner's parsing is
checked against what git actually emits rather than what it was written to expect: a hunk header carrying
trailing context (`@@ -3,3 +3,4 @@ line two`), an `index` line, and a second file in the same patch.

The credential in it is a fake, assembled from a repeated pattern.
