package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ecodeclub/ekit"
	"github.com/redis/go-redis/v9"
)

var (
	_ Session = &redisSession{}

	// ErrSessionExisted 表示尝试创建的Session已经存在。
	ErrSessionExisted = errors.New("session已存在")

	// ErrCreateSessionFailed 表示一个通用的创建失败，通常由底层Redis错误引起。
	ErrCreateSessionFailed = errors.New("创建session失败")

	// ErrDestroySessionFailed 表示销毁Session时发生错误。
	ErrDestroySessionFailed = errors.New("销毁session失败")

	// luaSetSessionIfNotExist 脚本用于原子性地创建Session。
	// 只有当Key不存在时，才会执行HSET操作。
	// 返回1表示创建成功，返回0表示Key已存在。
	// 使用 unpack(ARGV) 需要 Redis 4.0.0+，性能优于循环HSET。
	luaSetSessionIfNotExist = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
    redis.call('HSET', KEYS[1], unpack(ARGV))
    return 1
else
    return 0
end
`)
)

const (
	// keyFormat 定义了Session在Redis中的存储键格式，设为常量以方便管理和复用。
	keyFormat = "gateway:session:bizId:%d:userId:%d"
)

type Session interface {
	// UserInfo 返回当前Session关联的用户身份信息。
	UserInfo() UserInfo
	// Get 从Session中获取一个字段值。
	Get(ctx context.Context, key string) (ekit.AnyValue, error)
	// Set 向Session中设置一个字段键值对。
	Set(ctx context.Context, key, value string) error
	// Destroy 销毁整个Session。
	Destroy(ctx context.Context) error
}

// UserInfo 结构体定义了用户会话信息。
type UserInfo struct {
	BizID     int64 `json:"bizId"`
	UserID    int64 `json:"userId"`
	AutoClose bool  `json:"autoClose"` // 是否允许空闲时自动关闭连接
}

// redisSession 是 Session 接口的Redis实现。
type redisSession struct {
	userInfo UserInfo
	rdb      redis.Cmdable
	key      string
}

func newRedisSession(userInfo UserInfo, rdb redis.Cmdable) *redisSession {
	return &redisSession{
		userInfo: userInfo,
		rdb:      rdb,
		key:      fmt.Sprintf(keyFormat, userInfo.BizID, userInfo.UserID),
	}
}

// initialize 负责在Redis中实际创建Session。这是一个内部方法。
// 返回的 error 如果是 ErrSessionExisted，表示Session已存在。
func (s *redisSession) initialize(ctx context.Context) error {
	// 定义初始Session内容。
	// bizId和userId已在key中，这里不再冗余存储。
	// 使用RFC3339Nano格式存储时间，确保一致性。
	args := []any{
		"loginTime", time.Now().Format(time.RFC3339Nano),
	}

	// 执行Lua脚本
	res, err := luaSetSessionIfNotExist.Run(ctx, s.rdb, []string{s.key}, args...).Result()
	if err != nil {
		// 如果脚本执行出错，包装底层错误。
		return fmt.Errorf("%w: %w", ErrCreateSessionFailed, err)
	}

	created, ok := res.(int64)
	if !ok {
		// 正常情况下不会发生，但作为防御性编程，检查脚本返回类型。
		return fmt.Errorf("%w: 未知的脚本结果类型: %T", ErrCreateSessionFailed, res)
	}

	if created != 1 {
		// 如果脚本返回0，说明Session已存在。
		return ErrSessionExisted
	}

	return nil
}

func (s *redisSession) UserInfo() UserInfo { return s.userInfo }

func (s *redisSession) Get(ctx context.Context, key string) (ekit.AnyValue, error) {
	str, err := s.rdb.HGet(ctx, s.key, key).Result()
	if err != nil {
		// 处理Redis Nil错误（字段不存在）
		if errors.Is(err, redis.Nil) {
			return ekit.AnyValue{}, nil
		}
		return ekit.AnyValue{}, err
	}
	return ekit.AnyValue{Val: str}, nil
}

func (s *redisSession) Set(ctx context.Context, key, value string) error {
	// HSet 的 value 其实可以是 any 类型，go-redis会自动处理
	// 但传入结构体时它会被 go-redis 序列化成一种默认的字符串格式，这可能不是你期望的。反序列化时会遇到麻烦
	// 返回HSet的原始错误。
	return s.rdb.HSet(ctx, s.key, key, value).Err()
}

func (s *redisSession) Destroy(ctx context.Context) error {
	err := s.rdb.Del(ctx, s.key).Err()
	if err != nil {
		// 包装底层错误，提供更清晰的错误链。
		return fmt.Errorf("%w: %w", ErrDestroySessionFailed, err)
	}
	return nil
}

// Provider 是一个Session提供者的抽象接口。
type Provider interface {
	// Provide 获取或创建一个Session。
	// 无论Session是新创建的还是已存在的，都会返回一个可用的Session实例。
	// 返回的bool值表示Session是否为本次调用新创建的。
	Provide(ctx context.Context, info UserInfo) (session Session, isNew bool, err error)
}

// RedisSessionProvider 是 Provider 接口的Redis实现。
type RedisSessionProvider struct {
	rdb redis.Cmdable
}

// NewRedisSessionProvider 是 RedisSessionProvider 的构造函数。
func NewRedisSessionProvider(rdb redis.Cmdable) *RedisSessionProvider {
	return &RedisSessionProvider{rdb: rdb}
}

// Provide 实现 "GetOrCreate" 语义
func (r *RedisSessionProvider) Provide(ctx context.Context, userInfo UserInfo) (session Session, isNew bool, err error) {
	s := newRedisSession(userInfo, r.rdb)
	err = s.initialize(ctx)
	switch {
	case err == nil:
		// 没有错误，表示是新创建的
		return s, true, nil
	case errors.Is(err, ErrSessionExisted):
		// 如果错误是 ErrSessionExisted，这不是一个失败，返回session实例。
		return s, false, nil
	default:
		// 其他所有错误（如redis连接失败等）都是真正的失败。
		return nil, false, err
	}
}
