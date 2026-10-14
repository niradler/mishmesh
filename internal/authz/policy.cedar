permit (
  principal in Role::"owner",
  action in [
    Action::"agent:read", Action::"agent:write",
    Action::"endpoint:read", Action::"endpoint:write",
    Action::"quota:read", Action::"quota:write",
    Action::"member:read", Action::"member:manage",
    Action::"audit:read", Action::"status:read",
    Action::"policy:read", Action::"policy:write",
    Action::"service:read", Action::"service:write",
    Action::"connection:manage", Action::"host:exec"
  ],
  resource
);

permit (
  principal in Role::"admin",
  action in [
    Action::"agent:read", Action::"agent:write",
    Action::"endpoint:read", Action::"endpoint:write",
    Action::"quota:read", Action::"quota:write",
    Action::"member:read", Action::"member:manage",
    Action::"audit:read", Action::"status:read",
    Action::"policy:read", Action::"policy:write",
    Action::"service:read", Action::"service:write",
    Action::"connection:manage", Action::"host:exec"
  ],
  resource
)
unless { action == Action::"host:exec" };

permit (
  principal in Role::"member",
  action in [
    Action::"agent:read",
    Action::"endpoint:read",
    Action::"quota:read",
    Action::"member:read",
    Action::"audit:read",
    Action::"status:read",
    Action::"policy:read",
    Action::"service:read"
  ],
  resource
);
