# Netemlite

This repository contains unfinished experimental code that I was writing
to explore replacing [Gvisor](https://gvisor.dev/) from [ooni/netem](
https://github.com/ooni/netem) with the intention of making the implementation
leaner.

The general idea was that of replacing the TCP/IP implementation in Gvisor
with a significantly simpler one where we basically just send bags of bytes
around, we cannot change the latency, and we cannot drop TCP segments.

While exploring this possibility, I realized of a possible way to structure
the interaction between the underlying network and the "conn" layer that
is quite general and could be useful to me in the future. This is the main
reason why I have decided to kept the unfinished code of this PoC.
