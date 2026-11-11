# ADR 002: Integer ticks, lots and cents

Status: accepted.

Floating-point representation would make rounding, conservation and deterministic replay harder to audit. Base quantities use thousandth-coin lots; price ticks are cents per lot. Every notional is therefore an exact integer product with checked overflow. Both sides pay 10 bps, rounded up per execution, to an explicit fee account.

Buy reservations use a per-lot conservative fee bound to remain safe when one order fragments into many small fills. Excess reservation is released on commit. Average price is an explicitly rounded display field; exact trade prices remain available.

The consequence is a coarse $10-per-coin price grid in this simulation. Supporting finer real-world grids would require exact rational conversions, defined dust handling and additional invariant tests rather than simply introducing floats.
