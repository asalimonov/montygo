import asyncio
import os

from pydantic_monty import AsyncMontyWebsocket


async def double(x: int) -> int:
    return x * 2


async def main() -> None:
    async with AsyncMontyWebsocket(os.environ['MONTY_URL'], request_timeout=30.0) as pool:
        async with pool.checkout() as session:
            called = await session.feed_run(
                'await double(n) + 1',
                inputs={'n': 20},
                external_lookup={'double': double},
            )
            await session.feed_run('x = 41')
            state = await session.dump()
        async with pool.checkout() as restored:
            await restored.load_session(state)
            value = await restored.feed_run('x + 1')
    print(f'OK {called} {value}', flush=True)


asyncio.run(main())
