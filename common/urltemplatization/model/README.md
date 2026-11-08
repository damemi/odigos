# URL segment model

Scores each path segment as a parameter or a static literal. A logistic regression over hashed character, shape, and neighbor features produces `P(segment is a parameter)`. Live traffic learning stores that probability on each path-trie node. `Template` applies the trained threshold and replaces parameter segments with `{id}`.

Weights are embedded from `model.bin` (little-endian: bucket count, bias, threshold, then one float weight per bucket). `golden.json` holds the Python model's probabilities for a fixed set of paths. `TestMatchesPython` fails if Go features or hashing drift from that export.

## Retraining

Training is not in this module. It lives in [github.com/odigos-io/URL_templatization_model](https://github.com/odigos-io/URL_templatization_model).

`train/fetch.py` downloads APIs.guru OpenAPI specs into a path-template file. `train/train.py` fits the classifier and writes `model.bin` and `golden.json`:

```
python train/fetch.py <apis.guru-list.json> specs.json
python train/train.py specs.json <out_dir> [k8s_dump.json]
```

`features()` in `model.go` must stay byte-identical to `features()` in `train/train.py`, including MurmurHash3 bucketing compatible with scikit-learn `FeatureHasher`. After a retrain, copy the new `model.bin` and `golden.json` into this directory and run:

```
go test ./urltemplatization/model
```
