export type AccountPlan = "free" | "starter" | "pro" | "business";

export interface Account {
  id: string;
  email: string;
  plan: AccountPlan;
  status: string;
  role: TeamRole;
  name: string;
  display_name: string;
  timezone: string;
}

export interface PublicPlan {
  plan: AccountPlan;
  currency: string;
  price_minor: number;
  monitors: number;
  min_interval_seconds: number;
  regions: number;
  status_pages: number;
  custom_domains: number;
  alert_channels: number;
  retention_days: number;
  api_keys: number;
  monthly_api_requests: number;
  team_members: number;
}

export type TeamRole = "owner" | "admin" | "member";

export interface Member {
  principal_id: string;
  email: string;
  display_name: string;
  role: TeamRole;
  is_you: boolean;
  joined_at: string | null;
}

export interface MemberInvite {
  id: string;
  email: string;
  role: TeamRole;
  expires_at: string | null;
  created_at: string | null;
}

export interface TeamResponse {
  members: Member[];
  invites: MemberInvite[];
  limit: number;
  your_role: TeamRole;
  can_manage: boolean;
  seats_taken: number;
}

export interface WorkspaceAccount {
  account_id: string;
  name: string;
  role: TeamRole;
  plan: AccountPlan;
  is_current: boolean;
}

export interface MetricLimit {
  used: number;
  limit: number;
}

export interface Usage {
  plan: string;
  period: string;
  api_requests: MetricLimit;
  api_keys: MetricLimit;
  members: MetricLimit;
  retention_days: number;
}

export interface APIKey {
  id: string;
  name: string;
  prefix: string;
  created_at: string | null;
  last_used_at: string | null;
  expires_at: string | null;
  revoked_at: string | null;
  status: string;
}

export type MonitorKind = "http" | "keyword" | "tcp" | "heartbeat";

export type MonitorState = "pending" | "up" | "down" | "paused";

export interface Monitor {
  id: string;
  name: string;
  kind: MonitorKind;
  target: string | null;
  ping_url?: string;
  grace_seconds: number;
  method: string;
  headers: Record<string, string>;
  body: string | null;
  expected_statuses: number[];
  keyword: string | null;
  keyword_absent: boolean;
  follow_redirects: boolean;
  interval_seconds: number;
  timeout_ms: number;
  regions: string[];
  cert_warn_days: number;
  state: MonitorState;
  state_changed_at: string;
  last_checked_at: string | null;
  last_status_code: number | null;
  last_response_ms: number | null;
  last_error: string | null;
  cert_expires_at: string | null;
  paused: boolean;
  created_at: string;
  updated_at: string;
  uptime?: { "1d"?: number; "7d"?: number; "30d"?: number };
}

export interface MonitorInput {
  name: string;
  kind: MonitorKind;
  target?: string;
  grace_seconds?: number;
  method?: string;
  headers?: Record<string, string>;
  body?: string | null;
  expected_statuses?: number[];
  keyword?: string | null;
  keyword_absent?: boolean;
  follow_redirects?: boolean;
  interval_seconds?: number;
  timeout_ms?: number;
  regions?: string[];
  cert_warn_days?: number;
  paused?: boolean;
}

export interface CheckResult {
  region: string;
  checked_at: string;
  up: boolean;
  status_code: number | null;
  total_ms: number | null;
  dns_ms: number | null;
  connect_ms: number | null;
  tls_ms: number | null;
  ttfb_ms: number | null;
  error: string | null;
  cert_expires_at: string | null;
}

export type SeriesRange = "1h" | "24h" | "7d" | "30d";

export interface SeriesPoint {
  t: string;
  region: string;
  checks: number;
  failures: number;
  avg_ms: number | null;
  max_ms: number | null;
}

export interface MonitorSeries {
  range: SeriesRange;
  bucket_seconds: number;
  points: SeriesPoint[];
}

export interface DailyUptime {
  day: string;
  checks: number;
  failures: number;
  uptime: number | null;
  avg_ms: number | null;
}

export interface MonitorDaily {
  days: number;
  items: DailyUptime[];
}

export interface ProbeRegions {
  regions: string[];
  default: string;
  online?: string[];
}

export type ChannelKind = "email" | "telegram" | "slack" | "webhook";

export interface ChannelConfig {
  emails?: string[];
  chat_id?: string;
  topic_id?: number;
  webhook_url?: string;
  url?: string;
  signing_secret_set?: boolean;
}

export interface AlertChannel {
  id: string;
  kind: ChannelKind;
  name: string;
  config: ChannelConfig;
  enabled: boolean;
  all_monitors: boolean;
  monitor_ids: string[];
  last_delivered_at: string | null;
  last_error: string | null;
  created_at: string;
  signing_secret?: string;
}

export interface AlertChannelList {
  data: AlertChannel[];
  telegram_available: boolean;
  telegram_bot: string;
}

export interface ChannelInput {
  kind?: ChannelKind;
  name?: string;
  emails?: string[];
  chat_id?: string;
  topic_id?: number;
  webhook_url?: string;
  url?: string;
  enabled?: boolean;
  all_monitors?: boolean;
  monitor_ids?: string[];
}

export type DeliveryEvent = "down" | "up" | "certificate" | "test";

export interface ChannelDelivery {
  id: string;
  event: DeliveryEvent;
  monitor_id: string | null;
  monitor_name: string | null;
  status: "pending" | "delivered" | "failed";
  attempts: number;
  sent_at: string | null;
  last_error: string | null;
  created_at: string;
}

export interface Incident {
  id: string;
  monitor_id: string;
  monitor_name: string;
  started_at: string;
  resolved_at: string | null;
  cause: string;
  regions_down: string[];
  acknowledged_at: string | null;
  duration_seconds: number;
}

export type MaintenanceState = "scheduled" | "active" | "completed";

export interface MaintenanceWindow {
  id: string;
  title: string;
  starts_at: string;
  ends_at: string;
  monitor_ids: string[];
  all_monitors: boolean;
  state: MaintenanceState;
}

export interface MaintenanceInput {
  title: string;
  starts_at: string;
  ends_at: string;
  monitor_ids: string[];
}

export type StatusTheme = "auto" | "light" | "dark";

export interface StatusPageDomain {
  hostname: string;
  verified: boolean;
  verified_at: string | null;
  cname_target: string;
}

export interface StatusPageComponent {
  monitor_id: string;
  monitor_name: string;
  display_name: string | null;
  group_name: string | null;
  state: MonitorState;
}

export interface StatusPage {
  id: string;
  slug: string;
  title: string;
  description: string | null;
  homepage_url: string | null;
  logo_url: string | null;
  theme: StatusTheme;
  show_response_times: boolean;
  published: boolean;
  items: number;
  public_url: string;
  custom_domain: StatusPageDomain | null;
  created_at: string;
  components?: StatusPageComponent[];
}

export interface StatusPageInput {
  title?: string;
  slug?: string;
  description?: string;
  homepage_url?: string;
  logo_url?: string;
  theme?: StatusTheme;
  show_response_times?: boolean;
  published?: boolean;
  custom_domain?: string;
  monitor_ids?: string[];
}

export type StatusPostKind = "incident" | "maintenance";
export type StatusPostImpact = "none" | "minor" | "major" | "critical";

export interface StatusPostUpdate {
  id: string;
  status: string;
  body: string;
  created_at: string;
}

export interface StatusPost {
  id: string;
  kind: StatusPostKind;
  title: string;
  impact: StatusPostImpact;
  status: string;
  starts_at: string;
  ends_at: string | null;
  resolved_at: string | null;
  created_at: string;
  updates: StatusPostUpdate[];
}
