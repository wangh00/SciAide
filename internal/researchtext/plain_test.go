package researchtext

import "testing"

func TestPlainPreservesStatistics(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`<h4>Results</h4>β=-0.72, p<.005), each class improved 0.72 points.<h4>Conclusion</h4>Associated.`, `Results β=-0.72, p<.005), each class improved 0.72 points. Conclusion Associated.`},
		{`<p>p< 0.001, np<sup>2</sup>=0.599; x < 3 and y > 2.</p>`, `p< 0.001, np 2 =0.599; x < 3 and y > 2.`},
		{`<jats:p data-id="a>b">P &lt; 0.01 &amp; CI &gt; 0</jats:p>`, `P < 0.01 & CI > 0`},
		{`A<br/>B<!-- comment -->C`, `A B C`},
		{`p<.005 and p<=0.01`, `p<.005 and p<=0.01`},
	} {
		if got := Plain(tc.input); got != tc.want {
			t.Errorf("Plain(%q)=%q; want %q", tc.input, got, tc.want)
		}
	}
}
