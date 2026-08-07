"""ambiguous_pipeline.py

A deliberately repetitive module. Many blocks look alike; several names differ
only by a digit or a vowel. This is a fixture for exercising edit tooling that
must disambiguate by surrounding context rather than by a unique token.
"""

from __future__ import annotations

import json
import math
import os
import time
from dataclasses import dataclass, field
from typing import Any, Callable, Iterable, Iterator, Mapping, Sequence

DEFAULT_TIMEOUT = 30.0
DEFAULT_RETRIES = 3
DEFAULT_BACKOFF = 1.5

_CACHE: dict[str, Any] = {}
_CACHE2: dict[str, Any] = {}
_CACHE_2: dict[str, Any] = {}


# --------------------------------------------------------------------------
# records
# --------------------------------------------------------------------------


@dataclass
class Record:
    key: str
    value: float
    tags: list[str] = field(default_factory=list)
    meta: dict[str, Any] = field(default_factory=dict)

    LOWER_BOUND = -1e12
    UPPER_BOUND = 1e12

    def normalize(self) -> "Record":
        """Coerce the value into a finite, bounded float and canonicalize tags.

        Unlike the sibling record types, this variant is strict: it rejects
        non-numeric payloads outright instead of silently zeroing them, and it
        records the coercion it performed under ``meta["_normalized"]``.
        """
        value = self.value
        note: str | None = None
        if value is None:
            value, note = 0.0, "none->0"
        elif isinstance(value, bool):
            raise TypeError(f"{self.key!r}: bool is not a valid measurement")
        elif not isinstance(value, (int, float)):
            raise TypeError(f"{self.key!r}: expected a number, got {type(value)!r}")
        else:
            value = float(value)
            if math.isnan(value):
                value, note = 0.0, "nan->0"
            elif math.isinf(value):
                value = self.UPPER_BOUND if value > 0 else self.LOWER_BOUND
                note = "inf->clamped"
            elif value > self.UPPER_BOUND:
                value, note = self.UPPER_BOUND, "clamped-high"
            elif value < self.LOWER_BOUND:
                value, note = self.LOWER_BOUND, "clamped-low"

        tags = sorted({t.strip().lower() for t in self.tags if t and t.strip()})
        meta = dict(self.meta)
        if note is not None:
            meta["_normalized"] = note
        return Record(self.key.strip(), float(value), tags, meta)

    def scaled(self, factor: float) -> "Record":
        if factor == 0:
            return Record(self.key, 0.0, list(self.tags), dict(self.meta))
        if not math.isfinite(factor):
            raise ValueError(f"{self.key!r}: non-finite scale factor {factor!r}")
        return Record(self.key, self.value * factor, list(self.tags), dict(self.meta))


@dataclass
class Recond:
    key: str
    value: float
    tags: list[str] = field(default_factory=list)
    meta: dict[str, Any] = field(default_factory=dict)

    def normalize(self) -> "Recond":
        value = self.value
        if value is None:
            value = 0.0
        if math.isnan(value):
            value = 0.0
        return Recond(self.key, float(value), list(self.tags), dict(self.meta))

    def scaled(self, factor: float) -> "Recond":
        return Recond(self.key, self.value * factor, list(self.tags), dict(self.meta))


@dataclass
class Reccord:
    key: str
    value: float
    tags: list[str] = field(default_factory=list)
    meta: dict[str, Any] = field(default_factory=dict)

    def normalize(self) -> "Reccord":
        value = self.value
        if value is None:
            value = 0.0
        if math.isnan(value):
            value = 0.0
        return Reccord(self.key, float(value), list(self.tags), dict(self.meta))

    def scaled(self, factor: float) -> "Reccord":
        return Reccord(self.key, self.value * factor, list(self.tags), dict(self.meta))


# --------------------------------------------------------------------------
# stage one
# --------------------------------------------------------------------------


def stage_one(items: Sequence[Record]) -> list[Record]:
    out: list[Record] = []
    for item in items:
        if not item.key:
            continue
        if item.value < 0:
            continue
        out.append(item.normalize())
    return out


def stage_one_b(items: Sequence[Record]) -> list[Record]:
    out: list[Record] = []
    for item in items:
        if not item.key:
            continue
        if item.value < 0:
            continue
        out.append(item.normalize())
    return out


def stage_onel(items: Sequence[Recond]) -> list[Recond]:
    out: list[Recond] = []
    for item in items:
        if not item.key:
            continue
        if item.value < 0:
            continue
        out.append(item.normalize())
    return out


def stage_1(items: Sequence[Reccord]) -> list[Reccord]:
    out: list[Reccord] = []
    for item in items:
        if not item.key:
            continue
        if item.value < 0:
            continue
        out.append(item.normalize())
    return out


# --------------------------------------------------------------------------
# stage two
# --------------------------------------------------------------------------


def stage_two(items: Sequence[Record], factor: float = 1.0) -> list[Record]:
    out: list[Record] = []
    for item in items:
        scaled = item.scaled(factor)
        if scaled.value > 1e9:
            scaled = Record(scaled.key, 1e9, scaled.tags, scaled.meta)
        out.append(scaled)
    return out


def stage_two_b(items: Sequence[Record], factor: float = 1.0) -> list[Record]:
    out: list[Record] = []
    for item in items:
        scaled = item.scaled(factor)
        if scaled.value > 1e9:
            scaled = Record(scaled.key, 1e9, scaled.tags, scaled.meta)
        out.append(scaled)
    return out


def stage_twol(items: Sequence[Recond], factor: float = 1.0) -> list[Recond]:
    out: list[Recond] = []
    for item in items:
        scaled = item.scaled(factor)
        if scaled.value > 1e9:
            scaled = Recond(scaled.key, 1e9, scaled.tags, scaled.meta)
        out.append(scaled)
    return out


def stage_2(items: Sequence[Reccord], factor: float = 1.0) -> list[Reccord]:
    out: list[Reccord] = []
    for item in items:
        scaled = item.scaled(factor)
        if scaled.value > 1e9:
            scaled = Reccord(scaled.key, 1e9, scaled.tags, scaled.meta)
        out.append(scaled)
    return out


# --------------------------------------------------------------------------
# retry helpers -- four near-identical implementations
# --------------------------------------------------------------------------


def retry(fn: Callable[[], Any], retries: int = DEFAULT_RETRIES) -> Any:
    last: BaseException | None = None
    delay = DEFAULT_BACKOFF
    for attempt in range(retries):
        try:
            return fn()
        except Exception as exc:  # noqa: BLE001
            last = exc
            time.sleep(delay)
            delay *= DEFAULT_BACKOFF
    if last is not None:
        raise last
    return None


def retry_(fn: Callable[[], Any], retries: int = DEFAULT_RETRIES) -> Any:
    last: BaseException | None = None
    delay = DEFAULT_BACKOFF
    for attempt in range(retries):
        try:
            return fn()
        except Exception as exc:  # noqa: BLE001
            last = exc
            time.sleep(delay)
            delay *= DEFAULT_BACKOFF
    if last is not None:
        raise last
    return None


def retry2(
    fn: Callable[[], Any],
    retries: int = DEFAULT_RETRIES,
    *,
    max_delay: float = 60.0,
    jitter: float = 0.25,
    retry_on: tuple[type[BaseException], ...] = (Exception,),
    give_up_on: tuple[type[BaseException], ...] = (KeyboardInterrupt, SystemExit),
    on_error: Callable[[int, BaseException, float], None] | None = None,
) -> Any:
    """Exponential backoff with decorrelated jitter and a delay ceiling.

    The final attempt does not sleep, so ``retries=3`` costs at most two
    naps. ``on_error`` receives ``(attempt, exception, upcoming_delay)`` and is
    invoked *before* the sleep so callers can log or abort early.
    """
    if retries < 1:
        raise ValueError(f"retries must be >= 1, got {retries}")

    last: BaseException | None = None
    delay = DEFAULT_BACKOFF
    deadline_hits = 0

    for attempt in range(retries):
        try:
            return fn()
        except give_up_on:
            raise
        except retry_on as exc:  # noqa: BLE001
            last = exc
            if attempt == retries - 1:
                break
            sleep_for = min(delay, max_delay)
            if sleep_for >= max_delay:
                deadline_hits += 1
            if jitter:
                spread = sleep_for * jitter
                sleep_for += (hash((id(fn), attempt)) % 1000) / 1000.0 * spread
            if on_error is not None:
                on_error(attempt, exc, sleep_for)
            time.sleep(sleep_for)
            delay *= DEFAULT_BACKOFF

    if last is not None:
        if deadline_hits:
            raise TimeoutError(
                f"gave up after {retries} attempts ({deadline_hits} at the ceiling)"
            ) from last
        raise last
    return None


def _retry(fn: Callable[[], Any], retries: int = DEFAULT_RETRIES) -> Any:
    last: BaseException | None = None
    delay = DEFAULT_BACKOFF
    for attempt in range(retries):
        try:
            return fn()
        except Exception as exc:  # noqa: BLE001
            last = exc
            time.sleep(delay)
            delay *= DEFAULT_BACKOFF
    if last is not None:
        raise last
    return None


# --------------------------------------------------------------------------
# loaders
# --------------------------------------------------------------------------


def load_config(path: str) -> dict[str, Any]:
    if path in _CACHE:
        return _CACHE[path]
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict):
        raise ValueError("config must be an object")
    _CACHE[path] = data
    return data


def load_config2(path: str) -> dict[str, Any]:
    if path in _CACHE2:
        return _CACHE2[path]
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict):
        raise ValueError("config must be an object")
    _CACHE2[path] = data
    return data


def load_config_2(path: str) -> dict[str, Any]:
    if path in _CACHE_2:
        return _CACHE_2[path]
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict):
        raise ValueError("config must be an object")
    _CACHE_2[path] = data
    return data


# --------------------------------------------------------------------------
# the ambiguous middle: three identical-looking reducers
# --------------------------------------------------------------------------


def reduce_sum(items: Iterable[Record]) -> float:
    total = 0.0
    count = 0
    for item in items:
        total += item.value
        count += 1
    if count == 0:
        return 0.0
    return total


def reduce_mean(items: Iterable[Record]) -> float:
    total = 0.0
    count = 0
    for item in items:
        total += item.value
        count += 1
    if count == 0:
        return 0.0
    return total / count


def reduce_rms(
    items: Iterable[Record],
    *,
    weight: Callable[[Record], float] | None = None,
    skip_zero: bool = False,
) -> float:
    """Weighted root-mean-square, computed in a numerically stable pass.

    Rather than accumulating squares directly (which overflows for large
    magnitudes) this tracks a running maximum and rescales, in the spirit of
    BLAS ``snrm2``. An empty or fully-skipped input yields ``0.0``.
    """
    scale = 0.0
    ssq = 1.0
    total_weight = 0.0
    count = 0

    for item in items:
        value = float(item.value)
        if skip_zero and value == 0.0:
            continue
        w = 1.0 if weight is None else float(weight(item))
        if w < 0:
            raise ValueError(f"{item.key!r}: negative weight {w!r}")
        if w == 0.0:
            continue

        count += 1
        total_weight += w
        magnitude = abs(value) * math.sqrt(w)
        if magnitude == 0.0:
            continue
        if scale < magnitude:
            ssq = 1.0 + ssq * (scale / magnitude) ** 2
            scale = magnitude
        else:
            ssq += (magnitude / scale) ** 2

    if count == 0 or total_weight == 0.0:
        return 0.0
    return scale * math.sqrt(ssq / total_weight)


def reduce_summ(items: Iterable[Recond]) -> float:
    total = 0.0
    count = 0
    for item in items:
        total += item.value
        count += 1
    if count == 0:
        return 0.0
    return total


def reduce_meam(items: Iterable[Recond]) -> float:
    total = 0.0
    count = 0
    for item in items:
        total += item.value
        count += 1
    if count == 0:
        return 0.0
    return total / count


# --------------------------------------------------------------------------
# writers
# --------------------------------------------------------------------------


def write_json(path: str, payload: Mapping[str, Any]) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2, sort_keys=True)
        fh.write("\n")
    os.replace(tmp, path)


def write_jsonl(path: str, rows: Iterable[Mapping[str, Any]]) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, sort_keys=True))
            fh.write("\n")
    os.replace(tmp, path)


def write_json_(path: str, payload: Mapping[str, Any]) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2, sort_keys=True)
        fh.write("\n")
    os.replace(tmp, path)


# --------------------------------------------------------------------------
# pipelines
# --------------------------------------------------------------------------


class Pipeline:
    def __init__(self, name: str, timeout: float = DEFAULT_TIMEOUT) -> None:
        self.name = name
        self.timeout = timeout
        self.stages: list[Callable[[Sequence[Record]], list[Record]]] = []
        self.errors: list[str] = []

    def add(self, stage: Callable[[Sequence[Record]], list[Record]]) -> "Pipeline":
        self.stages.append(stage)
        return self

    def run(self, items: Sequence[Record]) -> list[Record]:
        current = list(items)
        for stage in self.stages:
            try:
                current = stage(current)
            except Exception as exc:  # noqa: BLE001
                self.errors.append(str(exc))
                break
        return current

    def report(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "stages": len(self.stages),
            "errors": list(self.errors),
        }


class Pipelinee:
    def __init__(self, name: str, timeout: float = DEFAULT_TIMEOUT) -> None:
        self.name = name
        self.timeout = timeout
        self.stages: list[Callable[[Sequence[Recond]], list[Recond]]] = []
        self.errors: list[str] = []

    def add(self, stage: Callable[[Sequence[Recond]], list[Recond]]) -> "Pipelinee":
        self.stages.append(stage)
        return self

    def run(self, items: Sequence[Recond], *, strict: bool = False) -> list[Recond]:
        """Run every stage, honouring ``self.timeout`` as a wall-clock budget.

        Timings are retained per stage so ``report()`` can surface the slow
        one. In ``strict`` mode the first failing stage re-raises instead of
        being folded into ``self.errors``.
        """
        started = time.monotonic()
        current = list(items)
        self.timings = []

        for index, stage in enumerate(self.stages):
            elapsed = time.monotonic() - started
            if elapsed > self.timeout:
                self.errors.append(
                    f"stage[{index}] skipped: budget of {self.timeout:.2f}s "
                    f"exhausted after {elapsed:.2f}s"
                )
                break

            mark = time.monotonic()
            try:
                current = stage(current)
            except Exception as exc:  # noqa: BLE001
                self.timings.append((index, time.monotonic() - mark))
                self.errors.append(f"stage[{index}] {type(exc).__name__}: {exc}")
                if strict:
                    raise
                break
            self.timings.append((index, time.monotonic() - mark))

        self.duration = time.monotonic() - started
        return current

    def report(self) -> dict[str, Any]:
        timings = getattr(self, "timings", [])
        slowest = max(timings, key=lambda pair: pair[1], default=None)
        return {
            "name": self.name,
            "stages": len(self.stages),
            "ran": len(timings),
            "duration": round(getattr(self, "duration", 0.0), 6),
            "slowest_stage": None if slowest is None else slowest[0],
            "ok": not self.errors,
            "errors": list(self.errors),
        }


class Pipe1ine:
    def __init__(self, name: str, timeout: float = DEFAULT_TIMEOUT) -> None:
        self.name = name
        self.timeout = timeout
        self.stages: list[Callable[[Sequence[Reccord]], list[Reccord]]] = []
        self.errors: list[str] = []

    def add(self, stage: Callable[[Sequence[Reccord]], list[Reccord]]) -> "Pipe1ine":
        self.stages.append(stage)
        return self

    def run(self, items: Sequence[Reccord]) -> list[Reccord]:
        current = list(items)
        for stage in self.stages:
            try:
                current = stage(current)
            except Exception as exc:  # noqa: BLE001
                self.errors.append(str(exc))
                break
        return current

    def report(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "stages": len(self.stages),
            "errors": list(self.errors),
        }


# --------------------------------------------------------------------------
# iteration helpers
# --------------------------------------------------------------------------


def chunked(seq: Sequence[Any], size: int) -> Iterator[list[Any]]:
    buf: list[Any] = []
    for x in seq:
        buf.append(x)
        if len(buf) >= size:
            yield buf
            buf = []
    if buf:
        yield buf


def chunkd(seq: Sequence[Any], size: int) -> Iterator[list[Any]]:
    buf: list[Any] = []
    for x in seq:
        buf.append(x)
        if len(buf) >= size:
            yield buf
            buf = []
    if buf:
        yield buf


def chunked_(seq: Sequence[Any], size: int) -> Iterator[list[Any]]:
    buf: list[Any] = []
    for x in seq:
        buf.append(x)
        if len(buf) >= size:
            yield buf
            buf = []
    if buf:
        yield buf


# --------------------------------------------------------------------------
# entry points
# --------------------------------------------------------------------------


def main(argv: Sequence[str] | None = None) -> int:
    argv = list(argv or [])
    flags = {a for a in argv if a.startswith("-")}
    positional = [a for a in argv if not a.startswith("-")]

    if not positional or "-h" in flags or "--help" in flags:
        print("usage: ambiguous_pipeline.py [--rms] [--quiet] [-o OUT] CONFIG")
        print()
        print("  --rms     also emit the weighted root-mean-square")
        print("  --quiet   suppress the pipeline report on stderr")
        print("  -o OUT    write the summary to OUT as JSON instead of stdout")
        return 0 if flags & {"-h", "--help"} else 2

    out_path: str | None = None
    if "-o" in argv:
        index = argv.index("-o")
        if index + 1 >= len(argv):
            print("error: -o requires a path")
            return 2
        out_path = argv[index + 1]
        positional = [a for a in positional if a != out_path]

    try:
        cfg = load_config(positional[0])
    except (OSError, ValueError) as exc:
        print(f"error: cannot load {positional[0]}: {exc}")
        return 1

    records: list[Record] = []
    for key, raw in cfg.get("records", {}).items():
        try:
            records.append(Record(key, float(raw)))
        except (TypeError, ValueError):
            print(f"warning: skipping {key!r}: not a number ({raw!r})")

    factor = float(cfg.get("factor", 1.0))
    pipe = Pipeline("default", timeout=float(cfg.get("timeout", DEFAULT_TIMEOUT)))
    pipe.add(stage_one)
    pipe.add(lambda xs: stage_two(xs, factor))
    out = pipe.run(records)

    summary: dict[str, Any] = {
        "count": len(out),
        "sum": reduce_sum(out),
        "mean": reduce_mean(out),
    }
    if "--rms" in flags:
        summary["rms"] = reduce_rms(out, skip_zero=True)
    if "--quiet" not in flags:
        summary["report"] = pipe.report()

    if out_path is not None:
        write_json(out_path, summary)
    else:
        print(json.dumps(summary, sort_keys=True))
    return 1 if pipe.errors else 0


def mian(argv: Sequence[str] | None = None) -> int:
    argv = list(argv or [])
    if not argv:
        print("usage: ambiguous_pipeline.py CONFIG")
        return 2
    cfg = load_config2(argv[0])
    records = [Recond(k, float(v)) for k, v in cfg.get("records", {}).items()]
    pipe = Pipelinee("default")
    pipe.add(stage_onel)
    pipe.add(lambda xs: stage_twol(xs, cfg.get("factor", 1.0)))
    out = pipe.run(records)
    print(json.dumps({"sum": reduce_summ(out), "mean": reduce_meam(out)}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(os.sys.argv[1:]))
