import {
  pairsToRecord,
  recordToPairs,
  type KVPair,
} from "@/components/common/KeyValueEditor";
import {
  upstreamFormFromPolicy,
  upstreamPolicyFields,
  type UpstreamForm,
} from "@/components/common/UpstreamFields";
import type { EndpointPolicy } from "@/api/types";

export interface PolicyForm {
  connectName: string;
  connectAgents: string[];
  reqAdd: KVPair[];
  reqRemove: string[];
  resAdd: KVPair[];
  resRemove: string[];
  hostHeader: string;
  stripPathPrefix: string;
  addPathPrefix: string;
  basicAuthUser: string;
  basicAuthPassword: string;
  basicAuthSet: boolean;
  ipAllow: string[];
  ipDeny: string[];
  forceHttps: boolean;
  maxBodyBytes: string;
  compression: boolean;
  oidcEnabled: boolean;
  oidcIssuer: string;
  oidcClientId: string;
  oidcClientSecret: string;
  oidcAllowedEmails: string[];
  oidcAllowedDomains: string[];
  upstream: UpstreamForm;
}

const REDACTED = "set";

export function policyFormFromPolicy(p?: EndpointPolicy | null): PolicyForm {
  return {
    connectName: p?.connect?.name ?? "",
    connectAgents: p?.connect?.allow_agents ?? [],
    reqAdd: recordToPairs(p?.request_headers_add),
    reqRemove: p?.request_headers_remove ?? [],
    resAdd: recordToPairs(p?.response_headers_add),
    resRemove: p?.response_headers_remove ?? [],
    hostHeader: p?.host_header ?? "",
    stripPathPrefix: p?.strip_path_prefix ?? "",
    addPathPrefix: p?.add_path_prefix ?? "",
    basicAuthUser: p?.basic_auth_user ?? "",
    basicAuthPassword: "",
    basicAuthSet: !!p?.basic_auth_hash,
    ipAllow: p?.ip_allow ?? [],
    ipDeny: p?.ip_deny ?? [],
    forceHttps: p?.force_https ?? false,
    maxBodyBytes: p?.max_body_bytes ? String(p.max_body_bytes) : "",
    compression: p?.compression ?? false,
    oidcEnabled: p?.oidc?.enabled ?? false,
    oidcIssuer: p?.oidc?.issuer ?? "",
    oidcClientId: p?.oidc?.client_id ?? "",
    oidcClientSecret: p?.oidc?.client_secret ?? "",
    oidcAllowedEmails: p?.oidc?.allowed_emails ?? [],
    oidcAllowedDomains: p?.oidc?.allowed_domains ?? [],
    upstream: upstreamFormFromPolicy(p),
  };
}

export function oidcSecretIsStored(form: PolicyForm): boolean {
  return form.oidcClientSecret === REDACTED;
}

export function policyFromForm(
  f: PolicyForm,
  isProxy: boolean,
  base?: EndpointPolicy | null,
): EndpointPolicy {
  const policy: EndpointPolicy = {
    ...base,
    connect:
      f.connectName || f.connectAgents.length > 0
        ? { name: f.connectName, allow_agents: f.connectAgents }
        : undefined,
    ...(isProxy ? upstreamPolicyFields(f.upstream) : {}),
    request_headers_add: pairsToRecord(f.reqAdd),
    request_headers_remove: f.reqRemove.filter(Boolean),
    response_headers_add: pairsToRecord(f.resAdd),
    response_headers_remove: f.resRemove.filter(Boolean),
    host_header: f.hostHeader || undefined,
    strip_path_prefix: f.stripPathPrefix || undefined,
    add_path_prefix: f.addPathPrefix || undefined,
    basic_auth_user: f.basicAuthUser || undefined,
    basic_auth_password: f.basicAuthPassword || undefined,
    ip_allow: f.ipAllow.filter(Boolean),
    ip_deny: f.ipDeny.filter(Boolean),
    force_https: f.forceHttps,
    max_body_bytes: f.maxBodyBytes ? Number(f.maxBodyBytes) : undefined,
    compression: f.compression,
    oidc: undefined,
  };
  if (!f.basicAuthUser) {
    policy.basic_auth_hash = undefined;
  }
  if (f.oidcEnabled || f.oidcIssuer || f.oidcClientId) {
    policy.oidc = {
      enabled: f.oidcEnabled,
      issuer: f.oidcIssuer,
      client_id: f.oidcClientId,
      client_secret: f.oidcClientSecret,
      allowed_emails: f.oidcAllowedEmails.filter(Boolean),
      allowed_domains: f.oidcAllowedDomains.filter(Boolean),
    };
  }
  return policy;
}
