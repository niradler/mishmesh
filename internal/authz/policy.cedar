permit (
  principal in Role::"owner",
  action,
  resource
);

permit (
  principal in Role::"admin",
  action,
  resource
);

permit (
  principal in Role::"member",
  action in [
    Action::"agent:read",
    Action::"endpoint:read",
    Action::"quota:read",
    Action::"member:read",
    Action::"audit:read",
    Action::"status:read",
    Action::"policy:read"
  ],
  resource
);
