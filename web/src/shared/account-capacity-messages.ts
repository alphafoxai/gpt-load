export const accountCapacityMessages = {
  'en-US': {
    title: 'Credential capacity and quota budget',
    description:
      'One global policy, applied independently to each credential across access keys, groups and models.',
    credential_concurrency_limit: 'Concurrent requests per credential',
    credential_rpm_limit: 'Dispatches per minute per credential',
    quota_budget: 'Claude subscription quota budget',
    concurrencyHelp:
      '0 means unlimited. Streams retain capacity until completion. Existing global, access-key and group limits still apply.',
    rpmHelp:
      '0 means unlimited. Each actual dispatch, including retries, consumes one slot in the sliding minute window.',
    budgetHelp:
      'An operator budget, not a provider safety guarantee. Applies only to Claude subscriptions. When enabled, missing or stale observations block dispatch. In-flight requests may overshoot the budget; usage is not reserved in advance.',
    enabled: 'Enable quota budget',
    five_hour_percent: 'Five-hour usage ceiling (%)',
    seven_day_percent: 'Seven-day usage ceiling (%)',
    observation_max_age_seconds: 'Maximum observation age (seconds)',
    budgetError:
      'Use whole-number percentages from 1 to 100 and an observation age from 1 to 86400 seconds.',
    suspended: 'Suspended (operator marked)',
    markSuspended: 'Mark suspended',
    clearSuspension: 'Clear suspension to disabled',
    suspensionHelp:
      'Local isolation records an operator-confirmed suspension; it does not change or verify provider enforcement. Clearing leaves the credential disabled. Enable it separately only after the provider has restored access.',
    suspensionFailed:
      'Unable to confirm the suspension change. Refresh to check the current status before retrying.',
    suspensionBlocked:
      'This credential is marked suspended. Clear suspension to disabled before enabling; provider actions are blocked.',
  },
  'zh-CN': {
    title: '凭据容量与配额预算',
    description: '统一的全局策略，针对每个凭据独立计算，跨访问密钥、分组和模型生效。',
    credential_concurrency_limit: '单凭据并发请求上限',
    credential_rpm_limit: '单凭据每分钟派发上限',
    quota_budget: 'Claude 订阅配额预算',
    concurrencyHelp: '0 表示不限。流式请求完成后才释放容量；现有全局、访问密钥及分组限制仍然生效。',
    rpmHelp: '0 表示不限。滑动分钟窗口内每次实际派发（包括重试）均占用一次额度。',
    budgetHelp:
      '这是运营预算，不是提供商的安全保证。仅适用于 Claude 订阅。启用后，缺失或过期的观测会阻止派发；进行中的请求可能超出预算，系统不预先预留用量。',
    enabled: '启用配额预算',
    five_hour_percent: '五小时用量上限（%）',
    seven_day_percent: '七天用量上限（%）',
    observation_max_age_seconds: '观测最长有效时间（秒）',
    budgetError: '百分比须为 1–100 的整数，观测有效时间须为 1–86400 秒的整数。',
    suspended: '已暂停（运营标记）',
    markSuspended: '标记为暂停',
    clearSuspension: '清除暂停并保持禁用',
    suspensionHelp:
      '本地隔离仅记录运营确认的暂停状态，不会改变或核实提供商的处置。清除后仍为禁用；仅在提供商恢复访问后另行启用。',
    suspensionFailed: '无法确认暂停状态是否已修改。请刷新核实当前状态后再重试。',
    suspensionBlocked: '此凭据已标记暂停。启用前须先清除暂停并保持禁用；提供商相关操作已被阻止。',
  },
  'ja-JP': {
    title: '認証情報の容量とクォータ予算',
    description:
      'アクセスキー、グループ、モデルを横断し、各認証情報に個別に適用するグローバルポリシーです。',
    credential_concurrency_limit: '認証情報ごとの同時リクエスト上限',
    credential_rpm_limit: '認証情報ごとの毎分送信上限',
    quota_budget: 'Claude サブスクリプションのクォータ予算',
    concurrencyHelp:
      '0 は無制限です。ストリームは完了まで容量を使用します。既存のグローバル、アクセスキー、グループ上限も適用されます。',
    rpmHelp:
      '0 は無制限です。スライディング分間枠では、再試行を含む実際の送信ごとに枠を消費します。',
    budgetHelp:
      '運用予算であり、プロバイダーの安全性を保証しません。Claude サブスクリプションのみ対象です。有効時は観測の欠落や期限切れで送信を停止します。使用量を事前予約しないため、実行中のリクエストが予算を超える場合があります。',
    enabled: 'クォータ予算を有効化',
    five_hour_percent: '5 時間の使用率上限（%）',
    seven_day_percent: '7 日間の使用率上限（%）',
    observation_max_age_seconds: '観測の最大有効期間（秒）',
    budgetError: '割合は 1～100、観測の有効期間は 1～86400 秒の整数を指定してください。',
    suspended: '停止中（運用者が指定）',
    markSuspended: '停止中として指定',
    clearSuspension: '停止指定を解除し無効にする',
    suspensionHelp:
      'ローカル隔離は運用者が確認した停止を記録するだけで、プロバイダーの措置を変更・検証しません。解除後も無効のままです。プロバイダーがアクセスを復旧した場合のみ、別途有効化してください。',
    suspensionFailed:
      '停止状態の変更を確認できません。再試行前に更新して現在の状態を確認してください。',
    suspensionBlocked:
      '停止中として指定されています。有効化前に停止指定を解除して無効状態にしてください。プロバイダー操作はブロックされています。',
  },
} as const
