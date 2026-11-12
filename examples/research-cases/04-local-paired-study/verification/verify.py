"""Read-only independent numeric check; never send the reference to the model."""
import csv
import json
import math
import statistics
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
rows = list(csv.DictReader((ROOT / 'materials/classroom-pre-post.csv').open(encoding='utf-8', newline='')))
assert len(rows) == 24 and len({r['student_id'] for r in rows}) == 24
pre = [int(r['pre_score']) for r in rows]
post = [int(r['post_score']) for r in rows]
assert all(0 <= v <= 100 for v in pre + post)
diff = [b - a for a, b in zip(pre, post)]
n = len(diff)
mean = statistics.mean(diff)
sd = statistics.stdev(diff)
se = sd / math.sqrt(n)
assert math.isclose(mean, statistics.mean(post) - statistics.mean(pre))
# A second variance identity checks the paired difference implementation.
assert math.isclose(sd * sd, (sum(d*d for d in diff) - sum(diff)**2/n)/(n-1))
# Student t critical value for df=23, two-sided 95%; sensitivity is not causal.
critical = 2.0686576104190406
result = dict(origin='synthetic software fixture; not real research evidence', n=n,
              pre_mean=statistics.mean(pre), post_mean=statistics.mean(post),
              post_minus_pre_mean=mean, difference_sd=sd,
              paired_t=mean/se, degrees_of_freedom=n-1,
              mean_difference_ci95=[mean-critical*se, mean+critical*se],
              increased=sum(d>0 for d in diff), unchanged=sum(d==0 for d in diff),
              decreased=sum(d<0 for d in diff))
expected = json.loads((ROOT / 'verification/reference-results.json').read_text(encoding='utf-8'))
for key, value in result.items():
    target = expected[key]
    if isinstance(value, float):
        assert math.isclose(value, target, rel_tol=1e-10, abs_tol=1e-10), key
    elif isinstance(value, list):
        assert all(math.isclose(a,b,rel_tol=1e-10,abs_tol=1e-10) for a,b in zip(value,target)), key
    else:
        assert value == target, key
print(json.dumps(result, ensure_ascii=False, indent=2))
