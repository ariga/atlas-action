from typing import List, Optional
from sqlalchemy import ForeignKey, String
from sqlalchemy.orm import Mapped, mapped_column, relationship, DeclarativeBase


class Base(DeclarativeBase):
    pass


class User(Base):
    __tablename__ = "user_account"
    id: Mapped[int] = mapped_column(primary_key=True)
    name: Mapped[str] = mapped_column(String(30))
    fullname: Mapped[Optional[str]] = mapped_column(String(30))
    nickname: Mapped[Optional[str]] = mapped_column(String(30))
    addresses: Mapped[List["Address"]] = relationship(
        back_populates="user", cascade="all, delete-orphan"
    )


class Address(Base):
    __tablename__ = "address"
    id: Mapped[int] = mapped_column(primary_key=True)
    email_address: Mapped[str] = mapped_column(String(30))
    neighborhood: Mapped[str] = mapped_column(String(30))
    user_id: Mapped[int] = mapped_column(ForeignKey("user_account.id"))
    user: Mapped["User"] = relationship(back_populates="addresses")