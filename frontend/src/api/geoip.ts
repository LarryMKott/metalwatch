import http from '@/utils/request'
import type { GeoIpStats } from '@/types/api'

// GeoIP 库管理接口（docs/04 §3）：库版本与覆盖统计。
export async function getGeoIpStats(): Promise<GeoIpStats> {
  const { data } = await http.get<GeoIpStats>('/geoip/stats')
  return data
}
