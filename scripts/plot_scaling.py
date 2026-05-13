#!/usr/bin/env python3
"""Render scaling and CPU% PNGs from the HPA metrics CSV.

Input CSV columns: t_seconds,pool,desired_replicas,current_replicas,cpu_pct_current,cpu_pct_target
"""
from __future__ import annotations

import argparse
import csv
import sys
from collections import defaultdict
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.ticker import MaxNLocator

POOL_COLORS = {"cpu": "#1f77b4", "gpu": "#d62728"}
POOL_LABELS = {"cpu": "ai-worker-cpu", "gpu": "ai-worker-gpu"}


def load_csv(path: Path):
    series = defaultdict(lambda: {"t": [], "desired": [], "current": [], "cpu": [], "target": None})
    with path.open() as f:
        reader = csv.DictReader(f)
        for row in reader:
            pool = row["pool"]
            try:
                t = int(row["t_seconds"])
            except ValueError:
                continue
            series[pool]["t"].append(t)
            series[pool]["desired"].append(int(row["desired_replicas"] or 0))
            series[pool]["current"].append(int(row["current_replicas"] or 0))
            cpu = row["cpu_pct_current"]
            series[pool]["cpu"].append(float(cpu) if cpu not in ("", None) else None)
            tgt = row["cpu_pct_target"]
            if tgt and series[pool]["target"] is None:
                series[pool]["target"] = float(tgt)
    return series


def plot_replicas(series, out_path: Path, title_suffix: str = ""):
    fig, ax = plt.subplots(figsize=(9, 4.5), dpi=140)
    for pool in ("cpu", "gpu"):
        s = series.get(pool)
        if not s or not s["t"]:
            continue
        color = POOL_COLORS[pool]
        ax.step(s["t"], s["current"], where="post", color=color,
                linewidth=2.2, label=f"{POOL_LABELS[pool]} — current")
        ax.step(s["t"], s["desired"], where="post", color=color,
                linewidth=1.2, linestyle="--", alpha=0.7,
                label=f"{POOL_LABELS[pool]} — desired")
    ax.set_xlabel("Time since test start (s)")
    ax.set_ylabel("Pod replicas")
    ax.set_title(f"HPA pod replicas over time{title_suffix}")
    ax.yaxis.set_major_locator(MaxNLocator(integer=True))
    ax.grid(True, alpha=0.3)
    ax.legend(loc="upper left", fontsize=9, framealpha=0.9)
    ax.set_ylim(bottom=0)
    fig.tight_layout()
    fig.savefig(out_path)
    plt.close(fig)


def plot_cpu(series, out_path: Path, title_suffix: str = ""):
    fig, ax = plt.subplots(figsize=(9, 4.5), dpi=140)
    drew_target = set()
    max_y = 100.0
    for pool in ("cpu", "gpu"):
        s = series.get(pool)
        if not s or not s["t"]:
            continue
        color = POOL_COLORS[pool]
        ts = [t for t, v in zip(s["t"], s["cpu"]) if v is not None]
        vs = [v for v in s["cpu"] if v is not None]
        if vs:
            max_y = max(max_y, max(vs) * 1.1)
        ax.plot(ts, vs, color=color, linewidth=2.0,
                label=f"{POOL_LABELS[pool]} — observed CPU%")
        if s["target"] and pool not in drew_target:
            ax.axhline(s["target"], color=color, linestyle=":", linewidth=1.2,
                       alpha=0.7, label=f"{POOL_LABELS[pool]} — target {int(s['target'])}%")
            drew_target.add(pool)
    ax.set_xlabel("Time since test start (s)")
    ax.set_ylabel("HPA-reported CPU utilization (%)")
    ax.set_title(f"HPA CPU utilization vs target{title_suffix}")
    ax.set_ylim(0, max_y)
    ax.grid(True, alpha=0.3)
    ax.legend(loc="upper left", fontsize=9, framealpha=0.9)
    fig.tight_layout()
    fig.savefig(out_path)
    plt.close(fig)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("csv", type=Path)
    p.add_argument("--out-dir", type=Path, default=Path("docs"))
    p.add_argument("--prefix", default="scaling")
    p.add_argument("--title-suffix", default="")
    args = p.parse_args()

    if not args.csv.exists():
        print(f"ERROR: CSV not found: {args.csv}", file=sys.stderr)
        sys.exit(1)

    series = load_csv(args.csv)
    if not series:
        print("ERROR: no rows parsed from CSV", file=sys.stderr)
        sys.exit(1)

    args.out_dir.mkdir(parents=True, exist_ok=True)
    replicas_path = args.out_dir / f"{args.prefix}_replicas.png"
    cpu_path = args.out_dir / f"{args.prefix}_cpu.png"

    plot_replicas(series, replicas_path, args.title_suffix)
    plot_cpu(series, cpu_path, args.title_suffix)

    print(f"wrote {replicas_path}")
    print(f"wrote {cpu_path}")


if __name__ == "__main__":
    main()
