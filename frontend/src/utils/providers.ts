/**
 * 管理台各视图共用的渠道下拉选项，顺序与后端 model.DefaultProviders 保持一致。
 * 新增渠道时同步追加到末尾：TokensView / SettingsView / PlaygroundView / LogsView / DashboardView
 * 都直接消费这份列表，无需逐个视图改动。
 *
 * 注意 `defaultProviders` 由本列表派生，因此它取的是「内置渠道全集」而不是
 * 「运行期默认路由」——后者由后端 `settings.default_providers` 决定，出厂仍是
 * 前八个通用搜索渠道，不含末尾的 context7（文档检索渠道，需显式指定）。
 */
export const providerOptions = [
  { label: 'Exa', value: 'exa' },
  { label: 'You.com', value: 'you' },
  { label: 'Jina', value: 'jina' },
  { label: 'Tavily', value: 'tavily' },
  { label: 'Firecrawl', value: 'firecrawl' },
  { label: 'Serper', value: 'serper' },
  { label: 'Brave Search', value: 'brave' },
  { label: 'Keenable', value: 'keenable' },
  { label: 'Context7', value: 'context7' }
]

export const defaultProviders = providerOptions.map((item) => item.value)

export function providerLabel(provider: string) {
  return providerOptions.find((item) => item.value === provider)?.label || provider
}
