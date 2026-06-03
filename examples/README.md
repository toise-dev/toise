# Examples

Example **consumers** of the Toise API. None of this is part of `toise-server` —
each example talks to Toise only over its public surfaces (GraphQL, MCP), the
same way a third-party integration would. Nothing here is compiled into the
server binary.

| Example | What it shows |
|---------|---------------|
| [graph-viz](./graph-viz/) | A dependency-free browser page that draws the entity/relation graph and streams live changes over GraphQL subscriptions. |
