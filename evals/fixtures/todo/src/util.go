package main

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }
